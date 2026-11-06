package reminder

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
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

// caldavDialContext 是出站连接实际使用的拨号器。默认走 safeDialContext；
// 单元测试要在本地 httptest 服务（监听 127.0.0.1）上跑，因此留一个可替换点，
// 生产路径不受影响。
var caldavDialContext = safeDialContext

func newCalDAVClient(username, password string) *caldavClient {
	return &caldavClient{
		http: &http.Client{
			Timeout: 25 * time.Second,
			// 日历地址由用户填写，属于「服务端替用户发起请求」的能力。没有这层
			// 校验时，任何登录用户都能把它当成内网探针（例如指向 127.0.0.1 上的
			// 管理面板或 169.254.169.254 的元数据服务）。
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					return caldavDialContext(ctx, network, address)
				},
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
			},
			// 重定向由 request() 自己逐跳重发：Go 的 Client 对 301/302/303 会把
			// 非 GET 方法改写成 GET 并丢掉请求体，PROPFIND 一旦被重定向就废了。
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		username: username,
		password: password,
	}
}

// blockedDialTarget 判断出站连接目标是否必须拒绝。
//
// 只拦「必然属于本机或链路本地」的地址：局域网自建 CalDAV（192.168/10.x）
// 是正当用法，私有网段保持放行。
func blockedDialTarget(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast()
}

// safeDialContext 先自行解析目标并校验，再直接连接已校验的 IP：这样即使域名
// 第二次解析给出别的地址（DNS rebinding），也绕不过检查。
func safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("解析日历服务器地址失败: %w", err)
	}
	if len(resolved) == 0 {
		return nil, errors.New("日历服务器地址无法解析")
	}
	for _, candidate := range resolved {
		if blockedDialTarget(candidate.IP) {
			return nil, errors.New("该地址指向本机或链路本地网络，已拒绝连接")
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, candidate := range resolved {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
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
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("服务器地址必须以 http:// 或 https:// 开头")
	}
	if parsed.User != nil {
		return "", errors.New("服务器地址不能包含账号信息，请在下方单独填写账号")
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	return parsed.String(), nil
}

// discoveryEntries 按优先级给出发现入口：先标准 RFC 6764 的
// /.well-known/caldav，再退回用户填写的地址。
//
// 钉钉的服务器根路径是网页应用（GET / 返回 200 HTML），对 PROPFIND 直接回
// 405；真正的 CalDAV 入口由 /.well-known/caldav 302 到 /dav/。不先走 well-known
// 就会在山门口被 405 挡下——这正是用户看到的「CalDAV 返回 405」。
func discoveryEntries(base string) []string {
	entries := make([]string, 0, 2)
	if parsed, err := url.Parse(base); err == nil && parsed.Host != "" {
		entries = append(entries, parsed.Scheme+"://"+parsed.Host+"/.well-known/caldav")
	}
	return append(entries, base)
}

// Discover walks principal → calendar-home → calendar collections and returns
// every readable collection. DingTalk's CalDAV dialog shows only a server
// address and credentials, so the URL cannot be typed in by hand.
func (c *caldavClient) Discover(ctx context.Context, server string) ([]string, error) {
	base, err := normalizeServerURL(server)
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
			// 只有「这个入口不是 CalDAV 端点」才值得换下一个入口；认证失败、
			// 网络错误必须立刻回给用户，否则真正的错因（例如密码不对）会被后一个
			// 入口的 405 覆盖掉——那正是「明明密码错了却报 405」的由来。
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
		return nil, errors.New("该地址不是可用的 CalDAV 服务：未返回 current-user-principal")
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

	collections, err := c.discoverCalendarCollections(ctx, homeURL)
	if err != nil {
		return nil, err
	}
	if len(collections) == 0 {
		return nil, errors.New("没有找到可读取的日历")
	}
	return collections, nil
}

// discoverHref issues a PROPFIND and returns the first non-empty href picker
// result, resolved against the URL the response actually came from (after
// redirects), so relative hrefs still land on the right host/path.
func (c *caldavClient) discoverHref(ctx context.Context, target, depth, body string, pick func(davProp) string) (string, error) {
	payload, finalURL, err := c.request(ctx, "PROPFIND", target, depth, body)
	if err != nil {
		return "", err
	}
	multi, err := decodeMultiStatus(payload)
	if err != nil {
		return "", err
	}
	for _, resp := range multi.Responses {
		for _, ps := range resp.Propstat {
			if href := strings.TrimSpace(pick(ps.Prop)); href != "" {
				return resolveHref(finalURL, href), nil
			}
		}
	}
	return "", nil
}

// discoverCalendarCollections lists children of the calendar home and returns
// every collection whose resourcetype contains <calendar/>.
//
// 必须全部返回：只取第一个的话，一个账号下的多个日历（个人/订阅/团队）只有
// 一个会被导入，其余日历的日程整批缺失——用户看到的就是「钉钉里有、应用里没有」。
func (c *caldavClient) discoverCalendarCollections(ctx context.Context, homeURL string) ([]string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`
	payload, finalURL, err := c.request(ctx, "PROPFIND", homeURL, "1", body)
	if err != nil {
		return nil, err
	}
	multi, err := decodeMultiStatus(payload)
	if err != nil {
		return nil, err
	}
	found := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, resp := range multi.Responses {
		if strings.TrimSpace(resp.Href) == "" {
			continue
		}
		for _, ps := range resp.Propstat {
			if ps.Prop.ResourceType.Calendar == nil {
				continue
			}
			href := resolveHref(finalURL, resp.Href)
			if !seen[href] {
				seen[href] = true
				found = append(found, href)
			}
		}
	}
	return found, nil
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

	payload, _, err := c.request(ctx, "REPORT", calendarURL, "1", body)
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

const (
	maxCalDAVRedirects = 3
	maxCalDAVBodyBytes = 8 << 20
)

// request 发起一次 CalDAV 请求，并自行逐跳跟随同主机重定向，返回响应体与
// 最终生效的地址。
//
// 不能用 http.Client 自动跟随：Go 对 301/302/303 会把非 GET 方法改写成 GET 并
// 丢弃请求体，而 /.well-known/caldav → /dav/ 这类跳转必须继续用 PROPFIND。
func (c *caldavClient) request(ctx context.Context, method, target, depth, body string) ([]byte, string, error) {
	current := target
	for hop := 0; hop <= maxCalDAVRedirects; hop++ {
		req, err := http.NewRequestWithContext(ctx, method, current, strings.NewReader(body))
		if err != nil {
			return nil, "", err
		}
		req.SetBasicAuth(c.username, c.password)
		req.Header.Set("Content-Type", "application/xml; charset=utf-8")
		if depth != "" {
			req.Header.Set("Depth", depth)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("连接 CalDAV 服务失败: %w", err)
		}
		// 限制读取体积：日历集合通常几十 KB，超过 8MB 说明响应异常。
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxCalDAVBodyBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, "", fmt.Errorf("读取 CalDAV 响应失败: %w", readErr)
		}

		if isCalDAVRedirect(resp.StatusCode) {
			next, err := sameHostRedirect(current, resp.Header.Get("Location"))
			if err != nil {
				return nil, "", err
			}
			current = next
			continue
		}

		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, "", &caldavStatusError{Status: resp.StatusCode, Message: "账号或密码不正确"}
		case resp.StatusCode == http.StatusNotFound:
			return nil, "", &caldavStatusError{Status: resp.StatusCode, Message: "日历地址不存在"}
		case resp.StatusCode >= 300:
			// 带上方法与实际请求地址：出现 405 时能立刻区分「地址不对（不是
			// CalDAV 集合）」和「服务器不允许该方法」。不回显响应体，避免把这个
			// 接口变成任意内网页面的读取通道。
			return nil, "", &caldavStatusError{Status: resp.StatusCode, Method: method, URL: current}
		}
		return payload, current, nil
	}
	return nil, "", errors.New("日历服务器重定向次数过多")
}

// caldavStatusError 表示「服务器应答了，但状态码不是 2xx」。调用方据此区分
// 「这个入口不是 CalDAV 端点（404/405，可以换下一个入口试试）」与「认证失败或
// 其它错误（必须直接告诉用户）」。Message 非空时优先作为用户可见文案。
type caldavStatusError struct {
	Status  int
	Method  string
	URL     string
	Message string
}

func (e *caldavStatusError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("CalDAV 返回 %d（%s）：%s %s", e.Status, http.StatusText(e.Status), e.Method, e.URL)
}

func isCalDAVRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// sameHostRedirect 把 Location 解析成绝对地址，并拒绝跨主机跳转：跨主机既可能
// 是地址填错，也会把 Basic 凭据带给第三方；遇到时直接提示用户填写最终地址。
func sameHostRedirect(current, location string) (string, error) {
	location = strings.TrimSpace(location)
	if location == "" {
		return "", errors.New("日历服务器返回了空的重定向地址")
	}
	base, err := url.Parse(current)
	if err != nil {
		return "", errors.New("当前 CalDAV 地址无效")
	}
	ref, err := url.Parse(location)
	if err != nil {
		return "", errors.New("日历服务器返回了无效的重定向地址")
	}
	target := base.ResolveReference(ref)
	if !strings.EqualFold(target.Scheme, base.Scheme) || !strings.EqualFold(target.Host, base.Host) {
		return "", fmt.Errorf("日历服务器重定向到了其他地址（%s://%s），请直接填写该地址", target.Scheme, target.Host)
	}
	return target.String(), nil
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
