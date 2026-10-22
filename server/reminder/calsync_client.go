package reminder

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// caldavClient speaks just enough CalDAV to discover a calendar collection and
// pull its events. It intentionally avoids a third-party client library: the
// surface used here is four request types, and a dependency would bring a much
// larger API than this integration needs.
type caldavClient struct {
	http     *http.Client
	username string
	password string
}

func newCalDAVClient(username, password string) *caldavClient {
	return &caldavClient{
		http:     &http.Client{Timeout: 25 * time.Second},
		username: username,
		password: password,
	}
}

// normalizeServerURL accepts what a user typically pastes from the DingTalk
// CalDAV dialog ("calendar.dingtalk.com") and turns it into a usable base URL.
func normalizeServerURL(server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", errors.New("请填写 CalDAV 服务器地址")
	}
	if !strings.Contains(server, "://") {
		server = "https://" + server
	}
	parsed, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("服务器地址无效: %w", err)
	}
	if parsed.Host == "" {
		return "", errors.New("服务器地址无效")
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	return parsed.String(), nil
}

// Discover walks principal → calendar-home → calendar collections and returns
// the first calendar collection it finds. DingTalk's CalDAV dialog shows only a
// server address and credentials, so the URL cannot be typed in by hand.
func (c *caldavClient) Discover(ctx context.Context, server string) (string, error) {
	base, err := normalizeServerURL(server)
	if err != nil {
		return "", err
	}

	principalBody := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	principal, err := c.discoverHref(ctx, base, "0", principalBody, func(p davProp) string {
		return p.CurrentUserPrincipal.Href
	})
	if err != nil {
		return "", err
	}
	if principal == "" {
		return "", errors.New("该地址不是可用的 CalDAV 服务：未返回 current-user-principal")
	}
	principalURL := resolveHref(base, principal)

	homeBody := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><c:calendar-home-set/></d:prop></d:propfind>`
	home, err := c.discoverHref(ctx, principalURL, "0", homeBody, func(p davProp) string {
		return p.CalendarHomeSet.Href
	})
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("该账号没有日历集合（calendar-home-set 为空）")
	}
	homeURL := resolveHref(base, home)

	found, err := c.discoverCalendarCollection(ctx, homeURL)
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("没有找到可读取的日历")
	}
	return found, nil
}

// discoverHref issues a PROPFIND and returns the first non-empty href picker
// result.
func (c *caldavClient) discoverHref(ctx context.Context, target, depth, body string, pick func(davProp) string) (string, error) {
	payload, err := c.request(ctx, "PROPFIND", target, depth, body)
	if err != nil {
		return "", err
	}
	multi, err := decodeMultiStatus(payload)
	if err != nil {
		return "", err
	}
	for _, resp := range multi.Responses {
		for _, ps := range resp.Propstat {
			if href := pick(ps.Prop); href != "" {
				return href, nil
			}
		}
	}
	return "", nil
}

// discoverCalendarCollection lists children of the calendar home and returns the
// first collection whose resourcetype contains <calendar/>.
func (c *caldavClient) discoverCalendarCollection(ctx context.Context, homeURL string) (string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`
	payload, err := c.request(ctx, "PROPFIND", homeURL, "1", body)
	if err != nil {
		return "", err
	}
	multi, err := decodeMultiStatus(payload)
	if err != nil {
		return "", err
	}
	for _, resp := range multi.Responses {
		if strings.TrimSpace(resp.Href) == "" {
			continue
		}
		for _, ps := range resp.Propstat {
			if ps.Prop.ResourceType.Calendar != nil {
				return resolveHref(homeURL, resp.Href), nil
			}
		}
	}
	return "", nil
}

// FetchEvents runs a calendar-query REPORT and returns the parsed VEVENTs.
func (c *caldavClient) FetchEvents(ctx context.Context, calendarURL string, from, to time.Time) ([]ICalEvent, error) {
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop><d:getetag/><c:calendar-data/></d:prop>
  <c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">
    <c:time-range start="%s" end="%s"/>
  </c:comp-filter></c:comp-filter></c:filter>
</c:calendar-query>`,
		from.UTC().Format("20060102T150405Z"), to.UTC().Format("20060102T150405Z"))

	payload, err := c.request(ctx, "REPORT", calendarURL, "1", body)
	if err != nil {
		return nil, err
	}
	multi, err := decodeMultiStatus(payload)
	if err != nil {
		return nil, err
	}

	loc := shanghai()
	var events []ICalEvent
	for _, resp := range multi.Responses {
		for _, ps := range resp.Propstat {
			if strings.TrimSpace(ps.Prop.CalendarData) == "" {
				continue
			}
			events = append(events, ParseICalEvents(ps.Prop.CalendarData, loc)...)
		}
	}
	return events, nil
}

func (c *caldavClient) request(ctx context.Context, method, target, depth, body string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	if depth != "" {
		req.Header.Set("Depth", depth)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 CalDAV 服务失败: %w", err)
	}
	defer resp.Body.Close()

	// 限制读取体积：日历集合通常几十 KB，超过 8MB 说明响应异常。
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 CalDAV 响应失败: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("账号或密码不正确")
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New("日历地址不存在")
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("CalDAV 返回 %d: %s", resp.StatusCode, truncateRunes(strings.TrimSpace(string(payload)), 120))
	}
	return payload, nil
}

type davMultiStatus struct {
	Responses []davResponse `xml:"response"`
}

type davResponse struct {
	Href     string        `xml:"href"`
	Propstat []davPropstat `xml:"propstat"`
}

type davPropstat struct {
	Prop   davProp `xml:"prop"`
	Status string  `xml:"status"`
}

type davProp struct {
	CurrentUserPrincipal davHref         `xml:"current-user-principal"`
	CalendarHomeSet      davHref         `xml:"calendar-home-set"`
	ResourceType         davResourceType `xml:"resourcetype"`
	CalendarData         string          `xml:"calendar-data"`
	GetETag              string          `xml:"getetag"`
}

type davHref struct {
	Href string `xml:"href"`
}

type davResourceType struct {
	Calendar *struct{} `xml:"calendar"`
}

func decodeMultiStatus(payload []byte) (davMultiStatus, error) {
	var multi davMultiStatus
	if err := xml.Unmarshal(payload, &multi); err != nil {
		return multi, fmt.Errorf("解析 CalDAV 响应失败: %w", err)
	}
	return multi, nil
}

// resolveHref turns an href returned by the server (often a path, sometimes a
// full URL) into an absolute URL based on the request that produced it.
func resolveHref(base, href string) string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return href
	}
	ref, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return href
	}
	return baseURL.ResolveReference(ref).String()
}
