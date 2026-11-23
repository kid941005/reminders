package reminder

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func newAppleStubClient(transport roundTripFunc) *caldavClient {
	client := newAppleCalDAVClient("user@example.com", "app-password")
	client.http.Transport = transport
	return client
}

func requireRequest(t *testing.T, r *http.Request, method string) {
	t.Helper()
	if r.Method != method {
		t.Fatalf("method = %q, want %q", r.Method, method)
	}
	if user, password, ok := r.BasicAuth(); !ok || user != "user@example.com" || password != "app-password" {
		t.Fatalf("basic auth = %q/%q ok=%v", user, password, ok)
	}
}

func TestPutReminderObjectCreateUsesIfNoneMatchAndHonoursLocation(t *testing.T) {
	const target = "https://caldav.icloud.com/calendars/u1/reminders/item.ics"
	const payload = "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"
	var calls int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		requireRequest(t, r, http.MethodPut)
		if got := r.Header.Get("If-None-Match"); got != "*" {
			t.Fatalf("If-None-Match = %q, want *", got)
		}
		if got := r.Header.Get("If-Match"); got != "" {
			t.Fatalf("If-Match = %q, want empty for create", got)
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/calendar") {
			t.Fatalf("Content-Type = %q, want text/calendar", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != payload {
			t.Fatalf("body = %q, want %q", string(body), payload)
		}
		return caldavStubResponse(http.StatusCreated, "", map[string]string{
			"ETag":     `"etag-1"`,
			"Location": "/calendars/u1/reminders/moved.ics",
		}), nil
	})

	etag, finalURL, err := newAppleStubClient(transport).PutReminderObject(context.Background(), target, payload, "", true)
	if err != nil {
		t.Fatalf("PutReminderObject: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if etag != `"etag-1"` {
		t.Fatalf("etag = %q", etag)
	}
	if want := "https://caldav.icloud.com/calendars/u1/reminders/moved.ics"; finalURL != want {
		t.Fatalf("finalURL = %q, want %q", finalURL, want)
	}
}

func TestPutReminderObjectUpdateUsesIfMatch(t *testing.T) {
	const target = "https://caldav.icloud.com/calendars/u1/reminders/item.ics"
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requireRequest(t, r, http.MethodPut)
		if got := r.Header.Get("If-Match"); got != `"etag-1"` {
			t.Fatalf("If-Match = %q, want the stored ETag", got)
		}
		if got := r.Header.Get("If-None-Match"); got != "" {
			t.Fatalf("If-None-Match = %q, want empty for update", got)
		}
		return caldavStubResponse(http.StatusNoContent, "", map[string]string{"ETag": `"etag-2"`}), nil
	})

	etag, finalURL, err := newAppleStubClient(transport).PutReminderObject(context.Background(), target, "payload", `"etag-1"`, false)
	if err != nil {
		t.Fatalf("PutReminderObject: %v", err)
	}
	if etag != `"etag-2"` || finalURL != target {
		t.Fatalf("etag/finalURL = %q/%q", etag, finalURL)
	}
}

func TestPutReminderObjectDefaultsToIfMatchAnyForUnconditionalUpdate(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("If-Match"); got != "*" {
			t.Fatalf("If-Match = %q, want *", got)
		}
		return caldavStubResponse(http.StatusNoContent, "", nil), nil
	})
	_, _, err := newAppleStubClient(transport).PutReminderObject(context.Background(), "https://caldav.icloud.com/x.ics", "payload", "", false)
	if err != nil {
		t.Fatalf("PutReminderObject: %v", err)
	}
}

func TestPutReminderObjectMapsStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"precondition", http.StatusPreconditionFailed, errAppleRemotePrecondition},
		{"conflict", http.StatusConflict, errAppleRemotePrecondition},
		{"missing", http.StatusNotFound, errAppleRemoteNotFound},
		{"unauthorized", http.StatusUnauthorized, errAppleAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return caldavStubResponse(tc.status, "server detail must not leak", nil), nil
			})
			_, _, err := newAppleStubClient(transport).PutReminderObject(context.Background(), "https://caldav.icloud.com/x.ics", "payload", `"etag"`, false)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestGetReminderObjectReturnsBodyAndETag(t *testing.T) {
	const body = "BEGIN:VCALENDAR\r\nBEGIN:VTODO\r\nUID:u1\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requireRequest(t, r, http.MethodGet)
		if got := r.Header.Get("Depth"); got != "" {
			t.Fatalf("Depth = %q, want empty for object GET", got)
		}
		return caldavStubResponse(http.StatusOK, body, map[string]string{"ETag": `"etag-3"`}), nil
	})
	gotBody, etag, err := newAppleStubClient(transport).GetReminderObject(context.Background(), "https://caldav.icloud.com/x.ics")
	if err != nil {
		t.Fatalf("GetReminderObject: %v", err)
	}
	if string(gotBody) != body || etag != `"etag-3"` {
		t.Fatalf("body/etag = %q/%q", string(gotBody), etag)
	}
}

func TestGetReminderObjectETagUsesGetAndMapsNotFound(t *testing.T) {
	var calls int
	client := newAppleStubClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		requireRequest(t, r, http.MethodGet)
		return caldavStubResponse(http.StatusOK, "ignored", map[string]string{"ETag": `W/"weak-etag"`}), nil
	}))
	etag, err := client.GetReminderObjectETag(context.Background(), "https://caldav.icloud.com/x.ics")
	if err != nil || etag != `W/"weak-etag"` || calls != 1 {
		t.Fatalf("etag=%q calls=%d err=%v", etag, calls, err)
	}

	missing := newAppleStubClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return caldavStubResponse(http.StatusNotFound, "", nil), nil
	}))
	if _, err := missing.GetReminderObjectETag(context.Background(), "https://caldav.icloud.com/x.ics"); !errors.Is(err, errAppleRemoteNotFound) {
		t.Fatalf("err = %v, want errAppleRemoteNotFound", err)
	}
}

func TestDeleteReminderObjectTreatsNotFoundAsSuccess(t *testing.T) {
	var calls int
	client := newAppleStubClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		requireRequest(t, r, http.MethodDelete)
		return caldavStubResponse(http.StatusNotFound, "", nil), nil
	}))
	if err := client.DeleteReminderObject(context.Background(), "https://caldav.icloud.com/x.ics"); err != nil {
		t.Fatalf("DeleteReminderObject: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestDeleteReminderObjectSurfacesAuthFailure(t *testing.T) {
	client := newAppleStubClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return caldavStubResponse(http.StatusUnauthorized, "denied", nil), nil
	}))
	err := client.DeleteReminderObject(context.Background(), "https://caldav.icloud.com/x.ics")
	if !errors.Is(err, errAppleAuth) || strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v, want auth error without response body", err)
	}
}

func TestPutReminderObjectKeepsMethodAndConditionalHeadersAcrossRedirect(t *testing.T) {
	const target = "https://caldav.icloud.com/old.ics"
	const redirected = "https://p01-caldav.icloud.com/new.ics"
	var methods []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		methods = append(methods, r.Method)
		if r.URL.String() == target {
			return caldavStubResponse(http.StatusTemporaryRedirect, "", map[string]string{"Location": redirected}), nil
		}
		if r.URL.String() != redirected {
			t.Fatalf("unexpected redirect target %q", r.URL.String())
		}
		requireRequest(t, r, http.MethodPut)
		if got := r.Header.Get("If-Match"); got != `"etag-1"` {
			t.Fatalf("If-Match lost after redirect: %q", got)
		}
		if body, _ := io.ReadAll(r.Body); string(body) != "payload" {
			t.Fatalf("body lost after redirect: %q", string(body))
		}
		return caldavStubResponse(http.StatusOK, "", map[string]string{"ETag": `"etag-2"`}), nil
	})
	etag, finalURL, err := newAppleStubClient(transport).PutReminderObject(context.Background(), target, "payload", `"etag-1"`, false)
	if err != nil {
		t.Fatalf("PutReminderObject: %v", err)
	}
	if etag != `"etag-2"` || finalURL != redirected {
		t.Fatalf("etag/finalURL = %q/%q", etag, finalURL)
	}
	if len(methods) != 2 || methods[0] != http.MethodPut || methods[1] != http.MethodPut {
		t.Fatalf("methods = %v, want PUT,PUT", methods)
	}
}
