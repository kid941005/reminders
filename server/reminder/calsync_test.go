package reminder

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseICalEventsHandlesCommonShapes(t *testing.T) {
	data := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"BEGIN:VEVENT",
		"UID:evt-1",
		"SUMMARY:周会",
		"DTSTART;TZID=Asia/Shanghai:20261023T100000",
		"DTEND;TZID=Asia/Shanghai:20261023T110000",
		"RRULE:FREQ=WEEKLY;COUNT=5",
		"DESCRIPTION:第一行\\n第二行",
		"LOCATION:A 栋 3 层",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"UID:evt-2",
		"SUMMARY:全天事件",
		"DTSTART;VALUE=DATE:20261024",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"UID:evt-3",
		"SUMMARY:已取消",
		"STATUS:CANCELLED",
		"DTSTART:20261025T090000Z",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n")

	events := ParseICalEvents(data, mustShanghai(t))
	if len(events) != 3 {
		t.Fatalf("期望解析出 3 个事件，实际 %d", len(events))
	}

	first := events[0]
	if first.RepeatRule != "weekly" {
		t.Fatalf("RRULE FREQ=WEEKLY 应映射为 weekly，实际 %q", first.RepeatRule)
	}
	if first.Start.Hour() != 10 || first.Start.Day() != 23 {
		t.Fatalf("DTSTART 应为 10 月 23 日 10:00，实际 %s", first.Start)
	}
	if first.Description != "第一行\n第二行" {
		t.Fatalf("DESCRIPTION 未正确反转义：%q", first.Description)
	}

	if !events[1].AllDay {
		t.Fatal("VALUE=DATE 应识别为全天事件")
	}
	if events[2].Status != "CANCELLED" {
		t.Fatalf("STATUS 未解析：%q", events[2].Status)
	}
}

// 折叠行（CRLF + 空格）必须拼接回一条，否则长标题会被截断。
func TestParseICalEventsUnfoldsLines(t *testing.T) {
	data := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:x\r\nSUMMARY:很长的标\r\n 题\r\nDTSTART:20261025T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR"
	events := ParseICalEvents(data, time.UTC)
	if len(events) != 1 {
		t.Fatalf("期望 1 个事件，实际 %d", len(events))
	}
	if events[0].Summary != "很长的标题" {
		t.Fatalf("折叠行未拼接：%q", events[0].Summary)
	}
}

// TestCalendarSyncImportsUpdatesAndPrunes 用假的 CalDAV 服务跑完整链路：
// 发现 → 拉取 → 落库 → 幂等重跑 → 远端删除后本地软删。
func TestCalendarSyncImportsUpdatesAndPrunes(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()
	t.Setenv("REMINDER_DATA_KEY", "test-key")

	var includeSecond int32 = 1

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:"><d:response><d:href>/</d:href><d:propstat><d:prop>
<d:current-user-principal><d:href>/principals/u/</d:href></d:current-user-principal>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		case r.Method == "PROPFIND" && r.URL.Path == "/principals/u/":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>/principals/u/</d:href><d:propstat><d:prop>
<c:calendar-home-set><d:href>/calendars/u/</d:href></c:calendar-home-set>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		case r.Method == "PROPFIND" && r.URL.Path == "/calendars/u/":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
<d:response><d:href>/calendars/u/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>
<d:response><d:href>/calendars/u/main/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/><c:calendar/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`))
		case r.Method == "REPORT":
			events := `BEGIN:VCALENDAR
BEGIN:VEVENT
UID:evt-a
SUMMARY:周会
DTSTART;TZID=Asia/Shanghai:20261023T100000
END:VEVENT`
			if atomic.LoadInt32(&includeSecond) == 1 {
				events += `
BEGIN:VEVENT
UID:evt-b
SUMMARY:评审
DTSTART;TZID=Asia/Shanghai:20261024T140000
END:VEVENT`
			}
			events += `
END:VCALENDAR`
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
<d:response><d:href>/calendars/u/main/evt.ics</d:href><d:propstat><d:prop>
<c:calendar-data>` + events + `</c:calendar-data>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg, err := SaveCalendarSync(appDB, 1, CalendarSyncInput{
		Server: server.URL, Username: "u", Password: "p",
	})
	if err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	if !strings.Contains(cfg.CalendarURL, "/calendars/u/main/") {
		t.Fatalf("日历集合发现错误：%s", cfg.CalendarURL)
	}
	if cfg.Password == "p" || cfg.Password == "" {
		t.Fatal("密码必须加密保存")
	}

	imported, err := RunCalendarSync(appDB, 1)
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if imported != 2 {
		t.Fatalf("首次同步应导入 2 条，实际 %d", imported)
	}

	var list List
	if err := appDB.Where("user_id = ? AND name = ?", 1, syncListName).First(&list).Error; err != nil {
		t.Fatalf("未创建「%s」清单: %v", syncListName, err)
	}
	var count int64
	appDB.Model(&Reminder{}).Where("user_id = ? AND external_source = ? AND deleted_at IS NULL", 1, ExternalSourceDingTalk).Count(&count)
	if count != 2 {
		t.Fatalf("库中应有 2 条导入提醒，实际 %d", count)
	}

	// 幂等：再跑一次不应新增。
	if _, err := RunCalendarSync(appDB, 1); err != nil {
		t.Fatal(err)
	}
	appDB.Model(&Reminder{}).Where("user_id = ? AND external_source = ? AND deleted_at IS NULL", 1, ExternalSourceDingTalk).Count(&count)
	if count != 2 {
		t.Fatalf("重复同步产生了副本，实际 %d 条", count)
	}

	// 远端删掉一条：本地应软删除，而不是留下孤儿。
	atomic.StoreInt32(&includeSecond, 0)
	if _, err := RunCalendarSync(appDB, 1); err != nil {
		t.Fatal(err)
	}
	appDB.Model(&Reminder{}).Where("user_id = ? AND external_source = ? AND deleted_at IS NULL", 1, ExternalSourceDingTalk).Count(&count)
	if count != 1 {
		t.Fatalf("远端删除后本地应剩 1 条，实际 %d", count)
	}
}

func mustShanghai(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	return loc
}
