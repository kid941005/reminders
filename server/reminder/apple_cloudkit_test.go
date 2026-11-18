package reminder

import (
	"strings"
	"testing"
	"time"
)

func TestCKEncodeTitleRoundTrip(t *testing.T) {
	cases := []string{"买牛奶", "Call mom", "项目复盘 · 第三季度", "🧪 emoji reminder", strings.Repeat("长标题", 40)}
	for _, want := range cases {
		encoded, err := ckEncodeTitle(want)
		if err != nil {
			t.Fatalf("ckEncodeTitle(%q): %v", want, err)
		}
		if encoded == "" {
			t.Fatalf("ckEncodeTitle(%q) returned empty", want)
		}
		if got := ckExtractTitle(encoded); got != want {
			t.Fatalf("roundtrip mismatch: want %q, got %q", want, got)
		}
	}
}

func TestAppleReminderBackendOf(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", appleReminderBackendCalDAV},
		{"caldav", appleReminderBackendCalDAV},
		{"CloudKit", appleReminderBackendCloudKit},
		{" cloudkit ", appleReminderBackendCloudKit},
		{"unknown", appleReminderBackendCalDAV},
	}
	for _, tc := range cases {
		cfg := &AppleReminderSync{Backend: tc.in}
		if got := appleReminderBackendOf(cfg); got != tc.want {
			t.Fatalf("backend %q: want %q, got %q", tc.in, tc.want, got)
		}
	}
	if got := appleReminderBackendOf(nil); got != appleReminderBackendCalDAV {
		t.Fatalf("nil backend: want caldav, got %q", got)
	}
}

func TestAppleCloudKitReminderFields(t *testing.T) {
	due := time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC)
	completed := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	item := Reminder{Title: "交房租", Notes: "转账给房东", Priority: 3, DueAt: &due, CompletedAt: &completed}
	fields, err := appleCloudKitReminderFields(item, "list-1")
	if err != nil {
		t.Fatal(err)
	}
	if fields["DueDate"].(map[string]interface{})["value"] != due.UnixMilli() {
		t.Fatalf("DueDate mismatch: %#v", fields["DueDate"])
	}
	if fields["CompletionDate"].(map[string]interface{})["value"] != completed.UnixMilli() {
		t.Fatalf("CompletionDate mismatch: %#v", fields["CompletionDate"])
	}
	if fields["Priority"].(map[string]interface{})["value"] != 1 {
		t.Fatalf("Priority mismatch: %#v", fields["Priority"])
	}
	if fields["Completed"].(map[string]interface{})["value"] != true {
		t.Fatalf("Completed mismatch: %#v", fields["Completed"])
	}
	list := fields["List"].(map[string]interface{})["value"].(map[string]interface{})
	if list["recordName"] != "list-1" {
		t.Fatalf("List mismatch: %#v", list)
	}
}

func TestAppleCloudKitSessionJSONRoundTrip(t *testing.T) {
	sess := &appleCloudKitSession{CKBaseURL: "https://ckdatabasews.icloud.com", SessionToken: "token", Cookies: []appleCloudKitCookie{{Name: "X-APPLE-WEBAUTH-USER", Value: "abc", Domain: ".icloud.com", Path: "/", Secure: true}}}
	client, err := newAppleCloudKitClientFromSession(sess)
	if err != nil {
		t.Fatal(err)
	}
	if client.ckBase != "https://ckdatabasews.icloud.com/" {
		t.Fatalf("unexpected ckBase %q", client.ckBase)
	}
}
