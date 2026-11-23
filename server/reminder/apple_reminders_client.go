package reminder

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

var (
	errAppleRemoteNotFound     = errors.New("Apple 提醒事项条目不存在")
	errAppleRemotePrecondition = errors.New("Apple 提醒事项已被其他设备修改")
	errAppleAuth               = errors.New("Apple 认证失败")
)

// newAppleCalDAVClient reuses the generic CalDAV transport and SSRF checks, but
// allows the same-scheme iCloud shard redirects Apple uses during discovery.
func newAppleCalDAVClient(username, password string) *caldavClient {
	client := newCalDAVClient(username, password)
	client.redirectAllowed = isAppleCloudHost
	return client
}

func isAppleCloudHost(target *url.URL) bool {
	if target == nil {
		return false
	}
	host := strings.ToLower(target.Hostname())
	for _, zone := range []string{"icloud.com", "icloud.com.cn"} {
		if host == zone || strings.HasSuffix(host, "."+zone) {
			return true
		}
	}
	return false
}

// appleServerOrDefault accepts the web address users normally know
// (www.icloud.com / www.icloud.com.cn) and maps it to the matching CalDAV
// endpoint. China-mainland iCloud accounts live on *.icloud.com.cn; sending
// them to caldav.icloud.com can land in the wrong partition or be rejected
// when Apple redirects to a pXX-caldav.icloud.com.cn shard.
func appleServerOrDefault(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return appleReminderDefaultServer
	}
	host := server
	if parsed, err := url.Parse(server); err == nil && parsed.Hostname() != "" {
		host = parsed.Hostname()
	}
	switch strings.ToLower(strings.TrimSuffix(host, ".")) {
	case "www.icloud.com", "icloud.com":
		return appleReminderDefaultServer
	case "www.icloud.com.cn", "icloud.com.cn":
		return appleReminderDefaultServerCN
	}
	return server
}

// DiscoverReminderCollections walks principal → calendar-home and returns only
// lists whose supported-calendar-component-set advertises VTODO.
func (c *caldavClient) DiscoverReminderCollections(ctx context.Context, server string) ([]AppleReminderCollection, error) {
	base, err := normalizeServerURL(appleServerOrDefault(server))
	if err != nil {
		return nil, err
	}

	principalBody := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	var principal string
	var lastErr error
	for _, entry := range discoveryEntries(base) {
		href, err := c.discoverHref(ctx, entry, "0", principalBody, func(p davProp) string {
			return p.CurrentUserPrincipal.Href
		})
		if err != nil {
			var statusErr *caldavStatusError
			if errors.As(err, &statusErr) && (statusErr.Status == http.StatusNotFound || statusErr.Status == http.StatusMethodNotAllowed) {
				lastErr = err
				continue
			}
			return nil, err
		}
		if href != "" {
			principal = href
			break
		}
	}
	if principal == "" {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("该账号不是可用的 iCloud CalDAV 服务：未返回 current-user-principal")
	}
	principalURL := resolveHref(base, principal)

	homeBody := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><c:calendar-home-set/></d:prop></d:propfind>`
	home, err := c.discoverHref(ctx, principalURL, "0", homeBody, func(p davProp) string {
		return p.CalendarHomeSet.Href
	})
	if err != nil {
		return nil, err
	}
	if home == "" {
		return nil, errors.New("该账号没有日历集合（calendar-home-set 为空）")
	}
	homeURL := resolveHref(base, home)

	body := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><d:resourcetype/><d:displayname/><c:supported-calendar-component-set/></d:prop>
</d:propfind>`
	resp, err := c.doRequest(ctx, "PROPFIND", homeURL, "1", body, nil)
	if err != nil {
		return nil, err
	}
	if err := appleResponseError("PROPFIND", resp); err != nil {
		return nil, err
	}
	multi, err := decodeMultiStatus(resp.Body)
	if err != nil {
		return nil, err
	}
	found := make([]AppleReminderCollection, 0, 4)
	seen := map[string]bool{}
	for _, item := range multi.Responses {
		if strings.TrimSpace(item.Href) == "" {
			continue
		}
		for _, ps := range item.Propstat {
			if ps.Prop.ResourceType.Calendar == nil || !ps.Prop.supportsVTODO() {
				continue
			}
			collectionURL := resolveHref(resp.URL, item.Href)
			if seen[collectionURL] {
				continue
			}
			seen[collectionURL] = true
			name := strings.TrimSpace(ps.Prop.DisplayName)
			if name == "" {
				name = strings.TrimSuffix(path.Base(collectionURL), "/")
			}
			found = append(found, AppleReminderCollection{URL: collectionURL, Name: name})
		}
	}
	if len(found) == 0 {
		return nil, errors.New("没有找到可用的提醒事项列表，请先在 iCloud 中创建一个列表")
	}
	return found, nil
}

// PutReminderObject creates (If-None-Match: *) or updates (If-Match) a VTODO.
// It returns the new ETag and the final object URL when the server supplies them.
func (c *caldavClient) PutReminderObject(ctx context.Context, target, body, ifMatch string, ifNoneMatch bool) (string, string, error) {
	headers := http.Header{}
	headers.Set("Content-Type", "text/calendar; charset=utf-8")
	if ifNoneMatch {
		headers.Set("If-None-Match", "*")
	} else if strings.TrimSpace(ifMatch) != "" {
		headers.Set("If-Match", strings.TrimSpace(ifMatch))
	} else {
		headers.Set("If-Match", "*")
	}
	resp, err := c.doRequest(ctx, "PUT", target, "", body, headers)
	if err != nil {
		return "", "", err
	}
	switch {
	case resp.Status >= 200 && resp.Status < 300:
		finalURL := resp.URL
		// A 201 may advertise the authoritative object URL in Location; prefer
		// it so later conditional updates target the object the server actually
		// created instead of the submitted path.
		if location := strings.TrimSpace(resp.Header.Get("Location")); location != "" {
			finalURL = resolveHref(resp.URL, location)
		}
		return resp.Header.Get("ETag"), finalURL, nil
	case resp.Status == http.StatusPreconditionFailed, resp.Status == http.StatusConflict:
		return "", resp.URL, errAppleRemotePrecondition
	case resp.Status == http.StatusNotFound:
		return "", resp.URL, errAppleRemoteNotFound
	default:
		return "", resp.URL, appleResponseError("PUT", resp)
	}
}

// GetReminderObject fetches a VTODO together with its ETag. The body is useful
// for verification and for the future pull direction; conditional updates only
// need GetReminderObjectETag.
func (c *caldavClient) GetReminderObject(ctx context.Context, target string) ([]byte, string, error) {
	resp, err := c.doRequest(ctx, "GET", target, "", "", nil)
	if err != nil {
		return nil, "", err
	}
	switch {
	case resp.Status >= 200 && resp.Status < 300:
		return resp.Body, resp.Header.Get("ETag"), nil
	case resp.Status == http.StatusNotFound:
		return nil, "", errAppleRemoteNotFound
	default:
		return nil, "", appleResponseError("GET", resp)
	}
}

// GetReminderObjectETag refreshes an ETag after a lost conditional update.
func (c *caldavClient) GetReminderObjectETag(ctx context.Context, target string) (string, error) {
	_, etag, err := c.GetReminderObject(ctx, target)
	return etag, err
}

// DeleteReminderObject removes a VTODO. A missing object counts as success.
func (c *caldavClient) DeleteReminderObject(ctx context.Context, target string) error {
	resp, err := c.doRequest(ctx, "DELETE", target, "", "", nil)
	if err != nil {
		return err
	}
	switch {
	case resp.Status >= 200 && resp.Status < 300:
		return nil
	case resp.Status == http.StatusNotFound:
		return nil
	default:
		return appleResponseError("DELETE", resp)
	}
}

func appleResponseError(method string, resp caldavResponse) error {
	if resp.Status >= 200 && resp.Status < 300 {
		return nil
	}
	switch resp.Status {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w：Apple ID 或应用专用密码不正确", errAppleAuth)
	case http.StatusForbidden:
		return fmt.Errorf("%w：Apple 服务拒绝请求，请检查应用专用密码或 iCloud 权限", errAppleAuth)
	case http.StatusNotFound:
		return fmt.Errorf("%w：%s %s", errAppleRemoteNotFound, method, resp.URL)
	default:
		return &caldavStatusError{Status: resp.Status, Method: method, URL: resp.URL, Message: fmt.Sprintf("Apple 提醒事项服务返回 %d（%s）", resp.Status, http.StatusText(resp.Status))}
	}
}
