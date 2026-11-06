package reminder

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// WebDAV 的 PROPFIND 不在 net/http 的方法常量里。
const methodPropfind = "PROPFIND"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 用内存里的 Transport 替代真实网络：既能验证完整的发现链路，又不依赖任何端口。
func caldavStubResponse(status int, body string, header map[string]string) *http.Response {
	h := http.Header{}
	for key, value := range header {
		h.Set(key, value)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func caldavMultiStatus(inner string) string {
	return `<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">` + inner + `</d:multistatus>`
}

// 钉钉的根路径是网页应用（GET / 返回 HTML），对 PROPFIND 直接回 405；真正的
// CalDAV 入口由 /.well-known/caldav 302 到 /dav/。旧实现从根路径开始发现，
// 用户只会拿到「CalDAV 返回 405」。这个用例把真实拓扑固定下来，并断言重定向
// 之后方法仍然是 PROPFIND（Go 的 Client 会自动降级成 GET，必须自己处理）。
func TestDiscoverFollowsWellKnownAndKeepsPropfindMethod(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{} // path -> method

	roundTrip := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen[r.URL.Path] = r.Method
		mu.Unlock()

		switch r.URL.Path {
		case "/":
			// 网页应用只接受 GET/HEAD。
			return caldavStubResponse(http.StatusMethodNotAllowed, "", nil), nil
		case "/.well-known/caldav":
			if r.Method != methodPropfind {
				return caldavStubResponse(http.StatusMethodNotAllowed, "", nil), nil
			}
			return caldavStubResponse(http.StatusFound, "", map[string]string{"Location": "/dav/principals/"}), nil
		case "/dav/principals/":
			if r.Method != methodPropfind || r.Header.Get("Depth") != "0" {
				return caldavStubResponse(http.StatusMethodNotAllowed, "", nil), nil
			}
			return caldavStubResponse(http.StatusMultiStatus, caldavMultiStatus(
				`<d:response><d:href>/dav/principals/</d:href><d:propstat><d:prop><d:current-user-principal><d:href>/dav/principals/u1/</d:href></d:current-user-principal></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`), nil), nil
		case "/dav/principals/u1/":
			return caldavStubResponse(http.StatusMultiStatus, caldavMultiStatus(
				`<d:response><d:href>/dav/principals/u1/</d:href><d:propstat><d:prop><c:calendar-home-set><d:href>/dav/calendars/u1/</d:href></c:calendar-home-set></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`), nil), nil
		case "/dav/calendars/u1/":
			return caldavStubResponse(http.StatusMultiStatus, caldavMultiStatus(
				`<d:response><d:href>/dav/calendars/u1/main/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/><c:calendar/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`), nil), nil
		default:
			return caldavStubResponse(http.StatusNotFound, "", nil), nil
		}
	})

	// 复用生产客户端的配置（尤其是 CheckRedirect = ErrUseLastResponse），只替换传输层。
	client := newCalDAVClient("u", "p")
	client.http.Transport = roundTrip
	collections, err := client.Discover(context.Background(), "http://caldav.test")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if want := "http://caldav.test/dav/calendars/u1/main/"; len(collections) != 1 || collections[0] != want {
		t.Fatalf("collections = %v, want exactly [%q]", collections, want)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"/.well-known/caldav", "/dav/principals/", "/dav/principals/u1/", "/dav/calendars/u1/"} {
		if seen[path] != methodPropfind {
			t.Fatalf("%s 收到 %q，期望 PROPFIND（重定向后方法不能被降级成 GET）", path, seen[path])
		}
	}
}

// 认证失败必须原样抛给用户，不能被后一个入口的 405 覆盖——这正是用户看到
// 「密码错了却报 405」的原因。
func TestDiscoverSurfacesAuthFailureFromWellKnownEntry(t *testing.T) {
	roundTrip := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/.well-known/caldav":
			return caldavStubResponse(http.StatusFound, "", map[string]string{"Location": "/dav/principals/"}), nil
		case "/dav/principals/":
			return caldavStubResponse(http.StatusUnauthorized, "", nil), nil
		default:
			return caldavStubResponse(http.StatusMethodNotAllowed, "", nil), nil
		}
	})
	client := newCalDAVClient("u", "bad")
	client.http.Transport = roundTrip
	_, err := client.Discover(context.Background(), "http://caldav.test")
	if err == nil || err.Error() != "账号或密码不正确" {
		t.Fatalf("err = %v, want 账号或密码不正确", err)
	}
}

// 跨主机跳转必须拒绝：否则 Basic 凭据会被带到第三方主机。
func TestDiscoverRejectsCrossHostRedirect(t *testing.T) {
	roundTrip := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return caldavStubResponse(http.StatusFound, "", map[string]string{"Location": "https://evil.example.com/dav/"}), nil
	})
	// 复用生产客户端的配置（尤其是 CheckRedirect = ErrUseLastResponse），只替换传输层。
	client := newCalDAVClient("u", "p")
	client.http.Transport = roundTrip
	_, err := client.Discover(context.Background(), "http://caldav.test")
	if err == nil || !strings.Contains(err.Error(), "重定向到了其他地址") {
		t.Fatalf("期望拒绝跨主机重定向，实际 err = %v", err)
	}
}
