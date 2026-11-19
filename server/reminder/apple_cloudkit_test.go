package reminder

import (
	"encoding/json"
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

func TestAppleCloudKitTwoFactorOptionsDTO(t *testing.T) {
	options := &appleCloudKitTwoFactorOptions{
		TrustedDevices: []appleCloudKitTrustedDevice{{ID: "d1"}},
		TrustedPhoneNumbers: []appleCloudKitPhoneNumber{
			{ID: 7, NumberWithDialCode: "+86 •••• 88", PushMode: "sms"},
			{ID: 8, NumberWithDialCode: "+86 •••• 99"},
		},
		SecurityCode: appleCloudKitSecurityCodeInfo{Length: 6},
	}
	dto := options.dto()
	if dto == nil || dto.TrustedDeviceCount != 1 || len(dto.Phones) != 2 {
		t.Fatalf("unexpected dto: %#v", dto)
	}
	if dto.SMSAutoSent || dto.CodeLength != 6 {
		t.Fatalf("unexpected flags: %#v", dto)
	}
	if dto.Phones[1].Mode != "sms" {
		t.Fatalf("empty push mode should default to sms: %#v", dto.Phones[1])
	}

	auto := (&appleCloudKitTwoFactorOptions{
		NoTrustedDevices:    true,
		TrustedPhoneNumbers: []appleCloudKitPhoneNumber{{ID: 9, NumberWithDialCode: "+86 •••• 00"}},
	}).dto()
	if !auto.SMSAutoSent {
		t.Fatalf("single phone without trusted devices should be auto-sent: %#v", auto)
	}

	// A nil slice must serialize as [] so the settings page can read
	// options.phones.length without throwing and blanking the view.
	empty := (&appleCloudKitTwoFactorOptions{}).dto()
	if empty.Phones == nil {
		t.Fatal("phones must be an empty slice, not nil")
	}
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"phones":[]`) {
		t.Fatalf("empty phones should serialize as []: %s", raw)
	}
}

func TestAppleCloudKitPickPhone(t *testing.T) {
	options := &appleCloudKitTwoFactorOptions{TrustedPhoneNumbers: []appleCloudKitPhoneNumber{
		{ID: 1, NumberWithDialCode: "+86 111"},
		{ID: 2, NumberWithDialCode: "+86 222"},
	}}
	if phone, err := appleCloudKitPickPhone(options, 2); err != nil || phone.ID != 2 {
		t.Fatalf("pick by id: phone=%#v err=%v", phone, err)
	}
	if _, err := appleCloudKitPickPhone(options, 99); err == nil {
		t.Fatal("unknown phone id should fail")
	}
	if _, err := appleCloudKitPickPhone(options, 0); err == nil {
		t.Fatal("multiple phones without a selection should fail")
	}
	single := &appleCloudKitTwoFactorOptions{TrustedPhoneNumbers: []appleCloudKitPhoneNumber{{ID: 3}}}
	if phone, err := appleCloudKitPickPhone(single, 0); err != nil || phone.ID != 3 {
		t.Fatalf("single phone fallback: phone=%#v err=%v", phone, err)
	}
	if _, err := appleCloudKitPickPhone(nil, 0); err == nil {
		t.Fatal("nil options should fail")
	}
}

func TestAppleCloudKitServiceError(t *testing.T) {
	if got := appleCloudKitServiceError([]byte(`{"serviceErrors":[{"message":"Too many codes sent","code":"-28248"}]}`)); got != "：Too many codes sent" {
		t.Fatalf("unexpected service error %q", got)
	}
	if got := appleCloudKitServiceError([]byte(`{"service_errors":[{"message":"验证码错误"}]}`)); got != "：验证码错误" {
		t.Fatalf("unexpected snake_case service error %q", got)
	}
	if got := appleCloudKitServiceError([]byte("not-json")); got != "" {
		t.Fatalf("non-json body should not echo, got %q", got)
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
