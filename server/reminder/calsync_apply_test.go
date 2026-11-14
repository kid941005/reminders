package reminder

import (
	"strings"
	"testing"
	"time"
)

// 导入的日程不应该默认产生站内提醒：用户通常已经在日程来源里收到通知，
// 没有主动勾选渠道时只进列表，不创建渠道与投递任务。
func TestApplySyncEventsImportsWithoutChannels(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	cfg := CalendarSync{UserID: 507, ListID: 1, RemindBeforeMinutes: 15}
	due := time.Now().AddDate(0, 0, 2).Truncate(time.Minute)
	events := []ICalEvent{{UID: "no-channel-1", Summary: "静默日程", Start: due, RepeatRule: "none"}}

	imported, _, err := applySyncEvents(appDB, cfg, events, time.Now().AddDate(0, 0, -7), time.Now().AddDate(0, 0, 180), true)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 {
		t.Fatalf("imported=%d", imported)
	}

	var item Reminder
	if err := appDB.Where("external_uid = ?", "no-channel-1").First(&item).Error; err != nil {
		t.Fatal(err)
	}
	var channelCount, jobCount int64
	if err := appDB.Model(&ReminderChannel{}).Where("reminder_id = ?", item.ID).Count(&channelCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.Model(&DeliveryJob{}).Where("reminder_id = ?", item.ID).Count(&jobCount).Error; err != nil {
		t.Fatal(err)
	}
	if channelCount != 0 || jobCount != 0 {
		t.Fatalf("导入日程不应默认创建渠道或投递任务: channels=%d jobs=%d", channelCount, jobCount)
	}
}

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

// 同一个循环日程如果只改地点/描述、时间不变，CalDAV 会同时返回主事件和
// RECURRENCE-ID 例外。旧实现把例外也存成独立提醒，导致同一时刻出现两条待办、
// 推送两次。这里验证例外被合并进主事件，而不是各自成行。
func TestApplySyncEventsMergesSameTimeRecurrenceException(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	cfg := CalendarSync{UserID: 506, ListID: 1, RemindBeforeMinutes: 15}
	loc := shanghai()
	due := time.Now().In(loc).AddDate(0, 0, 7).Truncate(time.Minute)
	events := []ICalEvent{
		{UID: "series-merge", Summary: "微泵项目进度讨论", Start: due, RepeatRule: "weekly"},
		{UID: "series-merge", Summary: "微泵项目进度讨论", Start: due, RepeatRule: "weekly",
			RecurrenceID: due.Format(time.RFC3339), Location: "https://meeting.dingtalk.com/j/abc"},
	}

	imported, _, err := applySyncEvents(appDB, cfg, events, time.Now().AddDate(0, 0, -7), time.Now().AddDate(0, 0, 180), true)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 {
		t.Fatalf("同时间例外不应单独导入，imported=%d", imported)
	}
	var rows []Reminder
	if err := appDB.Where("user_id = ? AND external_source = ?", 506, ExternalSourceDingTalk).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("期望 1 条，实际 %d: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.ExternalUID != "series-merge" || row.RepeatRule != "weekly" {
		t.Fatalf("主事件被改坏: uid=%q rule=%q", row.ExternalUID, row.RepeatRule)
	}
	if !strings.Contains(row.Notes, "meeting.dingtalk.com") {
		t.Fatalf("例外里的地点没有合并进主事件: %q", row.Notes)
	}
}

// 例外可能指向未来的某一周，而不是「下一次」实例；只在下一实例上比对会漏掉，
// 等系列推进到那一周时又会冒出第二条。这里验证窗口内任意实例都能匹配。
func TestApplySyncEventsMergesFutureSameTimeRecurrenceException(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	cfg := CalendarSync{UserID: 508, ListID: 1}
	loc := shanghai()
	masterDue := time.Now().In(loc).AddDate(0, 0, 7).Truncate(time.Minute)
	exceptionDue := masterDue.AddDate(0, 0, 14)
	events := []ICalEvent{
		{UID: "series-future", Summary: "周会", Start: masterDue, RepeatRule: "weekly"},
		{UID: "series-future", Summary: "周会", Start: exceptionDue, RepeatRule: "weekly",
			RecurrenceID: exceptionDue.Format(time.RFC3339), Location: "会议室 A"},
	}
	imported, _, err := applySyncEvents(appDB, cfg, events, time.Now().AddDate(0, 0, -7), time.Now().AddDate(0, 0, 180), true)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 {
		t.Fatalf("未来实例的同时间例外也不应单独导入，imported=%d", imported)
	}
	var count int64
	if err := appDB.Model(&Reminder{}).Where("user_id = ? AND external_source = ?", 508, ExternalSourceDingTalk).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("期望 1 条，实际 %d", count)
	}
}

// 旧版本已经导入过的同时间例外，需要在下一次同步时被清掉（只清未完成的），
// 否则用户升级后仍会看到两条并收到两次推送。
func TestApplySyncEventsCleansLegacySameTimeException(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	const userID = 507
	loc := shanghai()
	due := time.Now().In(loc).AddDate(0, 0, 7).Truncate(time.Minute).UTC()
	rid := due.Format(time.RFC3339)
	master := Reminder{UserID: userID, ListID: 1, Title: "微泵项目进度讨论", DueAt: &due,
		RepeatRule: "weekly", Calendar: "solar", Version: 1,
		ExternalSource: ExternalSourceDingTalk, ExternalUID: "series-legacy"}
	duplicate := Reminder{UserID: userID, ListID: 1, Title: "微泵项目进度讨论", DueAt: &due,
		RepeatRule: "none", Calendar: "solar", Version: 1, Notes: "地点：https://meeting.dingtalk.com/j/abc",
		ExternalSource: ExternalSourceDingTalk, ExternalUID: "series-legacy#" + rid}
	if err := appDB.Create(&master).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.Create(&duplicate).Error; err != nil {
		t.Fatal(err)
	}

	events := []ICalEvent{
		{UID: "series-legacy", Summary: "微泵项目进度讨论", Start: due, RepeatRule: "weekly"},
		{UID: "series-legacy", Summary: "微泵项目进度讨论", Start: due, RepeatRule: "weekly",
			RecurrenceID: rid, Location: "https://meeting.dingtalk.com/j/abc"},
	}
	// prune=false 模拟部分日历读取失败：合并本身不依赖清理，仍应生效。
	imported, pruned, err := applySyncEvents(appDB, CalendarSync{UserID: userID, ListID: 1}, events,
		time.Now().AddDate(0, 0, -7), time.Now().AddDate(0, 0, 180), false)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 || pruned != 0 {
		t.Fatalf("imported=%d pruned=%d", imported, pruned)
	}

	var active, deleted int64
	if err := appDB.Model(&Reminder{}).Where("user_id = ? AND external_source = ? AND deleted_at IS NULL", userID, ExternalSourceDingTalk).Count(&active).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.Model(&Reminder{}).Where("user_id = ? AND external_source = ? AND deleted_at IS NOT NULL", userID, ExternalSourceDingTalk).Count(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	if active != 1 || deleted != 1 {
		t.Fatalf("active=%d deleted=%d", active, deleted)
	}
	var row Reminder
	if err := appDB.Where("user_id = ? AND external_source = ? AND deleted_at IS NULL", userID, ExternalSourceDingTalk).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(row.Notes, "meeting.dingtalk.com") {
		t.Fatalf("清理重复行时没有保留地点: %q", row.Notes)
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
