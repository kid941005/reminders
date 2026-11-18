package reminder

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func mustCreateReminder(t *testing.T, item Reminder) Reminder {
	t.Helper()
	if item.Title == "" {
		item.Title = "测试提醒"
	}
	if item.RepeatRule == "" {
		item.RepeatRule = "none"
	}
	if item.Calendar == "" {
		item.Calendar = "solar"
	}
	if item.Version == 0 {
		item.Version = 1
	}
	if err := appDB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	return item
}

func TestBuildAppleReminderCalendarEscapesAndIncludesFields(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	due := time.Date(2026, 11, 18, 10, 0, 0, 0, shanghai())
	completed := time.Date(2026, 11, 17, 8, 30, 0, 0, time.UTC)
	item := Reminder{
		Title:       "买牛奶;面包,鸡蛋\n第二行",
		Notes:       "备注\\路径",
		Priority:    2,
		DueAt:       &due,
		RepeatRule:  "weekly",
		Calendar:    "solar",
		Version:     3,
		CompletedAt: &completed,
		CreatedAt:   now.Add(-24 * time.Hour),
		UpdatedAt:   now,
	}
	out := buildAppleReminderCalendar(item, "uid-1", now)
	for _, want := range []string{
		"BEGIN:VTODO",
		"UID:uid-1",
		"SUMMARY:买牛奶\\;面包\\,鸡蛋\\n第二行",
		"DESCRIPTION:备注\\\\路径",
		"DUE:20261118T020000Z",
		"PRIORITY:5",
		"STATUS:COMPLETED",
		"PERCENT-COMPLETE:100",
		"COMPLETED:20261117T083000Z",
		"RRULE:FREQ=WEEKLY",
		"SEQUENCE:2",
		"END:VTODO",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("calendar missing %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line exceeds 75 octets (%d): %q", len(line), line)
		}
	}
}

func TestBuildAppleReminderCalendarAllDayUsesShanghaiDate(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	due := time.Date(2026, 11, 18, 23, 30, 0, 0, time.UTC)
	item := Reminder{Title: "全天", DueAt: &due, AllDay: true, RepeatRule: "none", Calendar: "solar"}
	out := buildAppleReminderCalendar(item, "uid-2", now)
	if !strings.Contains(out, "DUE;VALUE=DATE:20261119") {
		t.Fatalf("all-day due should use local date, got:\n%s", out)
	}
}

func TestBuildAppleReminderCalendarOmitsLunarRRule(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	item := Reminder{Title: "农历生日", RepeatRule: "yearly", Calendar: "lunar"}
	out := buildAppleReminderCalendar(item, "uid-3", now)
	if strings.Contains(out, "RRULE:") {
		t.Fatalf("lunar recurrence must not emit RRULE:\n%s", out)
	}
	if !strings.Contains(out, "STATUS:NEEDS-ACTION") {
		t.Fatalf("open reminder should be NEEDS-ACTION:\n%s", out)
	}
}

func TestAppleReminderPriorityMapping(t *testing.T) {
	cases := map[int]int{0: 0, 1: 9, 2: 5, 3: 1, 9: 0}
	for input, want := range cases {
		if got := appleReminderPriority(input); got != want {
			t.Fatalf("priority(%d) = %d, want %d", input, got, want)
		}
	}
}

func TestEnqueueAppleSyncSkipsUnconfiguredAndExternal(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	item := mustCreateReminder(t, Reminder{UserID: 7, Title: "本地"})
	if err := enqueueAppleSync(appDB, 7, item.ID, appleReminderActionUpsert); err != nil {
		t.Fatal(err)
	}
	var count int64
	appDB.Model(&AppleReminderOutbox{}).Count(&count)
	if count != 0 {
		t.Fatalf("no config should enqueue nothing, got %d", count)
	}

	disabled := false
	appDB.Create(&AppleReminderSync{UserID: 7, Server: appleReminderDefaultServer, AppleID: "a@b.c", Password: "x", CollectionURL: "https://x/", Enabled: disabled})
	appDB.Model(&AppleReminderSync{}).Where("user_id = ?", 7).Update("enabled", false)
	if err := enqueueAppleSync(appDB, 7, item.ID, appleReminderActionUpsert); err != nil {
		t.Fatal(err)
	}
	appDB.Model(&AppleReminderOutbox{}).Count(&count)
	if count != 0 {
		t.Fatalf("disabled config should enqueue nothing, got %d", count)
	}

	appDB.Model(&AppleReminderSync{}).Where("user_id = ?", 7).Update("enabled", true)
	external := mustCreateReminder(t, Reminder{UserID: 7, Title: "导入", ExternalSource: ExternalSourceDingTalk})
	if err := enqueueAppleSync(appDB, 7, external.ID, appleReminderActionUpsert); err != nil {
		t.Fatal(err)
	}
	appDB.Model(&AppleReminderOutbox{}).Count(&count)
	if count != 0 {
		t.Fatalf("external reminder should enqueue nothing, got %d", count)
	}

	if err := enqueueAppleSync(appDB, 7, item.ID, appleReminderActionUpsert); err != nil {
		t.Fatal(err)
	}
	if err := enqueueAppleSync(appDB, 7, item.ID, appleReminderActionDelete); err != nil {
		t.Fatal(err)
	}
	var row AppleReminderOutbox
	if err := appDB.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	appDB.Model(&AppleReminderOutbox{}).Count(&count)
	if count != 1 || row.Action != appleReminderActionDelete || row.Status != appleReminderStatusPending {
		t.Fatalf("outbox should coalesce to one delete, got count=%d action=%q status=%q", count, row.Action, row.Status)
	}
}

func TestEnqueueAppleReminderBackfill(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	open := mustCreateReminder(t, Reminder{UserID: 9, Title: "待办"})
	done := mustCreateReminder(t, Reminder{UserID: 9, Title: "已完成"})
	now := time.Now().UTC()
	appDB.Model(&Reminder{}).Where("id = ?", done.ID).Update("completed_at", now)
	linked := mustCreateReminder(t, Reminder{UserID: 9, Title: "已映射已完成"})
	appDB.Model(&Reminder{}).Where("id = ?", linked.ID).Update("completed_at", now)
	appDB.Create(&AppleReminderLink{UserID: 9, ReminderID: linked.ID, RemoteURL: "https://x/1.ics"})
	mustCreateReminder(t, Reminder{UserID: 9, Title: "导入", ExternalSource: ExternalSourceDingTalk})
	deleted := mustCreateReminder(t, Reminder{UserID: 9, Title: "已删除"})
	appDB.Model(&Reminder{}).Where("id = ?", deleted.ID).Update("deleted_at", now)
	appDB.Create(&AppleReminderLink{UserID: 9, ReminderID: deleted.ID, RemoteURL: "https://x/2.ics"})
	appDB.Create(&AppleReminderSync{UserID: 9, Server: appleReminderDefaultServer, AppleID: "a@b.c", Password: "x", CollectionURL: "https://x/", Enabled: true})
	appDB.Model(&AppleReminderSync{}).Where("user_id = ?", 9).Update("enabled", true)

	if err := enqueueAppleReminderBackfill(appDB, 9); err != nil {
		t.Fatal(err)
	}
	var rows []AppleReminderOutbox
	if err := appDB.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	got := map[uint]string{}
	for _, row := range rows {
		got[row.ReminderID] = row.Action
	}
	if len(got) != 3 || got[open.ID] != appleReminderActionUpsert || got[linked.ID] != appleReminderActionUpsert {
		t.Fatalf("backfill should queue open + linked completed as upsert, got %+v", got)
	}
	if got[deleted.ID] != appleReminderActionDelete {
		t.Fatalf("deleted linked reminder should be queued for remote delete, got %+v", got)
	}
	if _, ok := got[done.ID]; ok {
		t.Fatalf("backfill must skip plain completed rows, got %+v", got)
	}
}

func TestDeleteAppleReminderOutboxIfCurrent(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	row := AppleReminderOutbox{UserID: 1, ReminderID: 10, Action: appleReminderActionUpsert, Status: appleReminderStatusPending}
	if err := appDB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	var stored AppleReminderOutbox
	if err := appDB.First(&stored, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := deleteAppleReminderOutboxIfCurrent(appDB, stored); err != nil {
		t.Fatal(err)
	}
	var count int64
	appDB.Model(&AppleReminderOutbox{}).Count(&count)
	if count != 0 {
		t.Fatalf("current row should be deleted, count=%d", count)
	}

	stale := AppleReminderOutbox{UserID: 1, ReminderID: 11, Action: appleReminderActionUpsert, Status: appleReminderStatusPending}
	if err := appDB.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.First(&stale, stale.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.Model(&AppleReminderOutbox{}).Where("id = ?", stale.ID).Update("status", appleReminderStatusRetry).Error; err != nil {
		t.Fatal(err)
	}
	if err := deleteAppleReminderOutboxIfCurrent(appDB, stale); err != nil {
		t.Fatal(err)
	}
	appDB.Model(&AppleReminderOutbox{}).Where("id = ?", stale.ID).Count(&count)
	if count != 1 {
		t.Fatalf("stale row must be kept, count=%d", count)
	}
}

func TestDiscoverReminderCollectionsFiltersVTODO(t *testing.T) {
	roundTrip := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/.well-known/caldav":
			return caldavStubResponse(http.StatusMultiStatus, caldavMultiStatus(
				`<d:response><d:href>/.well-known/caldav</d:href><d:propstat><d:prop><d:current-user-principal><d:href>/principals/u1/</d:href></d:current-user-principal></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`), nil), nil
		case "/principals/u1/":
			return caldavStubResponse(http.StatusMultiStatus, caldavMultiStatus(
				`<d:response><d:href>/principals/u1/</d:href><d:propstat><d:prop><c:calendar-home-set><d:href>/calendars/u1/</d:href></c:calendar-home-set></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`), nil), nil
		case "/calendars/u1/":
			if r.Method != methodPropfind || r.Header.Get("Depth") != "1" {
				return caldavStubResponse(http.StatusMethodNotAllowed, "", nil), nil
			}
			return caldavStubResponse(http.StatusMultiStatus, caldavMultiStatus(
				`<d:response><d:href>/calendars/u1/reminders/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>提醒事项</d:displayname><c:supported-calendar-component-set><c:comp name="VTODO"/></c:supported-calendar-component-set></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`+
					`<d:response><d:href>/calendars/u1/events/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>日历</d:displayname><c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`+
					`<d:response><d:href>/calendars/u1/tasks/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/><c:calendar/></d:resourcetype><c:supported-calendar-component-set><c:comp name="VTODO"/></c:supported-calendar-component-set></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`), nil), nil
		default:
			return caldavStubResponse(http.StatusNotFound, "", nil), nil
		}
	})
	client := newAppleCalDAVClient("u", "p")
	client.http.Transport = roundTrip
	lists, err := client.DiscoverReminderCollections(context.Background(), "http://caldav.test")
	if err != nil {
		t.Fatalf("DiscoverReminderCollections: %v", err)
	}
	if len(lists) != 2 {
		t.Fatalf("expected 2 VTODO lists, got %+v", lists)
	}
	if lists[0].Name != "提醒事项" || lists[0].URL != "http://caldav.test/calendars/u1/reminders/" {
		t.Fatalf("unexpected first list %+v", lists[0])
	}
	if lists[1].Name != "tasks" {
		t.Fatalf("fallback name should be path base, got %+v", lists[1])
	}
}

func TestAppleRedirectWhitelist(t *testing.T) {
	client := newAppleCalDAVClient("u", "p")
	if _, err := client.redirectTarget("https://caldav.icloud.com/", "https://p01-caldav.icloud.com/123/principal/"); err != nil {
		t.Fatalf("iCloud shard redirect should be allowed: %v", err)
	}
	if _, err := client.redirectTarget("https://caldav.icloud.com/", "https://evil.example.com/"); err == nil {
		t.Fatal("cross-host redirect to non-iCloud host should be rejected")
	}
	if isAppleCloudHost(nil) || isAppleCloudHost(mustURL(t, "https://noticloud.com/")) || !isAppleCloudHost(mustURL(t, "https://p01-caldav.icloud.com/")) {
		t.Fatal("isAppleCloudHost must accept icloud.com and its subdomains")
	}
	if !isAppleCloudHost(mustURL(t, "https://p01-caldav.icloud.com.cn/")) {
		t.Fatal("isAppleCloudHost must accept China iCloud shards")
	}
	if isAppleCloudHost(mustURL(t, "https://evil.icloud.com.cn.example.com/")) {
		t.Fatal("isAppleCloudHost must reject lookalike hosts")
	}

	for input, want := range map[string]string{
		"":                           appleReminderDefaultServer,
		"www.icloud.com":             appleReminderDefaultServer,
		"https://icloud.com/":        appleReminderDefaultServer,
		"www.icloud.com.cn":          appleReminderDefaultServerCN,
		"https://www.icloud.com.cn/": appleReminderDefaultServerCN,
		"p01-caldav.icloud.com.cn":   "p01-caldav.icloud.com.cn",
	} {
		if got := appleServerOrDefault(input); got != want {
			t.Fatalf("appleServerOrDefault(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSaveAppleReminderLinkStoresAndUpdatesETag(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	item := mustCreateReminder(t, Reminder{UserID: 12, Title: "Apple link"})
	if err := saveAppleReminderLink(appDB, item, "uid-1", "https://caldav.icloud.com/1.ics", "https://caldav.icloud.com/", "etag-1"); err != nil {
		t.Fatal(err)
	}
	var link AppleReminderLink
	if err := appDB.Where("reminder_id = ?", item.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.ETag != "etag-1" {
		t.Fatalf("expected etag-1, got %q", link.ETag)
	}

	if err := saveAppleReminderLink(appDB, item, "uid-1", "https://caldav.icloud.com/1.ics", "https://caldav.icloud.com/", "etag-2"); err != nil {
		t.Fatal(err)
	}
	if err := appDB.Where("reminder_id = ?", item.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.ETag != "etag-2" {
		t.Fatalf("expected etag-2 after upsert, got %q", link.ETag)
	}
}
