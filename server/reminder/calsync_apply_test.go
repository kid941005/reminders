package reminder

import (
	"testing"
	"time"
)

// 多日历、循环日程推进、例外实例、清理窗口与「复活」是这次缺漏修复的核心，
// 这里全部用真实 SQLite 覆盖（不需要网络）。
func TestApplySyncEventsRollsRecurringMasterForward(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	cfg := CalendarSync{UserID: 501, ListID: 1, RemindBeforeMinutes: 15}
	loc := shanghai()
	// 主事件的 DTSTART 停在很久以前（每周一 10:00），这是循环会议的常见形态。
	past := time.Date(2024, time.March, 4, 10, 0, 0, 0, loc)
	events := []ICalEvent{{UID: "weekly-1", Summary: "周会", Start: past, RepeatRule: "weekly"}}

	imported, pruned, err := applySyncEvents(appDB, cfg, events, time.Now().AddDate(0, 0, -7), time.Now().AddDate(0, 0, 180), true)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 || pruned != 0 {
		t.Fatalf("imported=%d pruned=%d", imported, pruned)
	}

	var item Reminder
	if err := appDB.Where("external_uid = ?", "weekly-1").First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.DueAt == nil {
		t.Fatal("due_at 为空")
	}
	if item.DueAt.Before(time.Now().Add(-time.Hour)) {
		t.Fatalf("循环日程仍停在过去: %v", item.DueAt)
	}
	local := item.DueAt.In(loc)
	if local.Weekday() != time.Monday || local.Hour() != 10 || local.Minute() != 0 {
		t.Fatalf("推进后星期/时刻被改变: %v", local)
	}
}

func TestApplySyncEventsKeepsRecurrenceExceptions(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	cfg := CalendarSync{UserID: 502, ListID: 1, RemindBeforeMinutes: 15}
	loc := shanghai()
	mon := time.Now().AddDate(0, 0, 7)
	wed := mon.AddDate(0, 0, 2)
	events := []ICalEvent{
		{UID: "series-1", Summary: "周会", Start: mon, RepeatRule: "weekly"},
		{UID: "series-1", Summary: "周会（改期）", Start: wed, RepeatRule: "weekly", RecurrenceID: "2026-11-11T10:00:00+08:00"},
	}

	imported, _, err := applySyncEvents(appDB, cfg, events, time.Now().AddDate(0, 0, -7), time.Now().AddDate(0, 0, 180), true)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 2 {
		t.Fatalf("同一 UID 的主事件与例外实例应各存一条，imported=%d", imported)
	}
	var rows []Reminder
	if err := appDB.Where("user_id = ? AND external_source = ?", 502, ExternalSourceDingTalk).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(rows))
	}
	var foundException bool
	for _, row := range rows {
		if row.RepeatRule == "none" && row.Title == "周会（改期）" {
			foundException = true
			if row.DueAt == nil || row.DueAt.In(loc).Day() != wed.In(loc).Day() {
				t.Fatalf("例外实例的日期不对: %v", row.DueAt)
			}
		}
	}
	if !foundException {
		t.Fatalf("例外实例没有独立成行: %+v", rows)
	}
}

func TestApplySyncEventsResurrectsSoftDeletedRow(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	cfg := CalendarSync{UserID: 503, ListID: 1, RemindBeforeMinutes: 15}
	from, to := time.Now().AddDate(0, 0, -7), time.Now().AddDate(0, 0, 180)
	due := time.Now().Add(24 * time.Hour)
	events := []ICalEvent{{UID: "revive-1", Summary: "旧标题", Start: due, RepeatRule: "none"}}
	if _, _, err := applySyncEvents(appDB, cfg, events, from, to, true); err != nil {
		t.Fatal(err)
	}

	// 模拟「某次同步只拿到部分数据」把它清掉了。
	deletedAt := time.Now()
	if err := appDB.Model(&Reminder{}).Where("external_uid = ?", "revive-1").Update("deleted_at", &deletedAt).Error; err != nil {
		t.Fatal(err)
	}

	events[0].Summary = "新标题"
	if _, _, err := applySyncEvents(appDB, cfg, events, from, to, true); err != nil {
		t.Fatal(err)
	}

	var item Reminder
	if err := appDB.Where("external_uid = ?", "revive-1").First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.DeletedAt != nil {
		t.Fatal("被清理过的日程没有复活")
	}
	if item.Title != "新标题" {
		t.Fatalf("title = %q", item.Title)
	}
}

func TestApplySyncEventsPrunesOnlyInsideWindow(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	const userID = 504
	now := time.Now().UTC()
	from, to := now.AddDate(0, 0, -7), now.AddDate(0, 0, 180)
	seed := func(uid string, due time.Time) {
		d := due
		row := Reminder{UserID: userID, ListID: 1, Title: uid, DueAt: &d, RepeatRule: "none", Calendar: "solar", Version: 1,
			ExternalSource: ExternalSourceDingTalk, ExternalUID: uid}
		if err := appDB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed("seen-in-window", now.Add(time.Hour))
	seed("unseen-in-window", now.Add(2*time.Hour))
	seed("unseen-far-future", now.AddDate(1, 0, 0))

	events := []ICalEvent{{UID: "seen-in-window", Summary: "seen-in-window", Start: now.Add(time.Hour), RepeatRule: "none"}}
	imported, pruned, err := applySyncEvents(appDB, CalendarSync{UserID: userID, ListID: 1}, events, from, to, true)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 || pruned != 1 {
		t.Fatalf("imported=%d pruned=%d", imported, pruned)
	}

	state := map[string]bool{}
	var rows []Reminder
	if err := appDB.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		state[row.ExternalUID] = row.DeletedAt == nil
	}
	if !state["seen-in-window"] {
		t.Fatal("窗口内且本轮可见的条目被误删")
	}
	if state["unseen-in-window"] {
		t.Fatal("窗口内但远端已删除的条目没被清理")
	}
	if !state["unseen-far-future"] {
		t.Fatal("窗口外的条目被误删（会把未来日程整批干掉）")
	}
}

func TestCalendarSyncCollectionURLs(t *testing.T) {
	multi := CalendarSync{Collections: `["https://a/cal1/","https://a/cal2/"]`, CalendarURL: "https://a/cal1/"}
	if got := multi.collectionURLs(); len(got) != 2 || got[1] != "https://a/cal2/" {
		t.Fatalf("multi = %v", got)
	}
	legacy := CalendarSync{CalendarURL: "https://a/old/"}
	if got := legacy.collectionURLs(); len(got) != 1 || got[0] != "https://a/old/" {
		t.Fatalf("legacy = %v", got)
	}
	if got := (CalendarSync{}).collectionURLs(); got != nil {
		t.Fatalf("empty = %v", got)
	}
}

// 某个日历集合读取失败时必须跳过清理，否则会把它的日程整批误删。
func TestApplySyncEventsSkipsPruneWhenNotTrusted(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	const userID = 505
	now := time.Now().UTC()
	from, to := now.AddDate(0, 0, -7), now.AddDate(0, 0, 180)
	due := now.Add(3 * time.Hour)
	row := Reminder{UserID: userID, ListID: 1, Title: "另一个日历的日程", DueAt: &due, RepeatRule: "none", Calendar: "solar", Version: 1,
		ExternalSource: ExternalSourceDingTalk, ExternalUID: "other-calendar"}
	if err := appDB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}

	if _, pruned, err := applySyncEvents(appDB, CalendarSync{UserID: userID, ListID: 1}, nil, from, to, false); err != nil {
		t.Fatal(err)
	} else if pruned != 0 {
		t.Fatalf("prune=false 时不应清理，pruned=%d", pruned)
	}
	var reloaded Reminder
	if err := appDB.Where("external_uid = ?", "other-calendar").First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.DeletedAt != nil {
		t.Fatal("读取失败的一轮把条目删掉了")
	}
}
