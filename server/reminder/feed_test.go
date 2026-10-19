package reminder

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCalendarICSContent(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	const userID = 88
	loc, _ := time.LoadLocation("Asia/Shanghai")

	weekly := time.Date(2026, 11, 2, 9, 0, 0, 0, loc)
	if _, err := createReminder(appDB, userID, SaveReminderInput{
		Title: "每周例会", DueAt: &weekly, RepeatRule: "weekly", Channels: []string{ChannelInApp},
	}); err != nil {
		t.Fatal(err)
	}

	allDay := time.Date(2026, 11, 5, 0, 0, 0, 0, loc)
	if _, err := createReminder(appDB, userID, SaveReminderInput{
		Title: "全天事项", DueAt: &allDay, AllDay: true, RepeatRule: "none", Channels: []string{ChannelInApp},
	}); err != nil {
		t.Fatal(err)
	}

	doneDue := time.Date(2026, 11, 6, 9, 0, 0, 0, loc)
	done, err := createReminder(appDB, userID, SaveReminderInput{
		Title: "已完成的那条", DueAt: &doneDue, RepeatRule: "none", Channels: []string{ChannelInApp},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := completeReminder(appDB, userID, done.ID); err != nil {
		t.Fatal(err)
	}

	// 没有日期的提醒没有可落位的日子，不应出现在订阅里。
	if _, err := createReminder(appDB, userID, SaveReminderInput{
		Title: "没有日期", RepeatRule: "none", Channels: []string{ChannelInApp},
	}); err != nil {
		t.Fatal(err)
	}

	ics, err := buildCalendarICS(appDB, userID)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Count(ics, "BEGIN:VEVENT"); got != 2 {
		t.Fatalf("expected 2 events (weekly + all-day), got %d", got)
	}
	if strings.Contains(ics, "已完成的那条") {
		t.Fatal("completed reminders must not appear in the feed")
	}
	if strings.Contains(ics, "没有日期") {
		t.Fatal("undated reminders must not appear in the feed")
	}
	if !strings.Contains(ics, "RRULE:FREQ=WEEKLY") {
		t.Fatal("weekly repeat must map to FREQ=WEEKLY")
	}
	// 全天事件是左闭右开：11-05 开始，DTEND 必须落在 11-06。
	if !strings.Contains(ics, "DTSTART;VALUE=DATE:20261105") {
		t.Fatal("all-day DTSTART missing")
	}
	if !strings.Contains(ics, "DTEND;VALUE=DATE:20261106") {
		t.Fatal("all-day DTEND must be the next day")
	}
	// 定时事件统一输出 UTC：上海 09:00 即 01:00Z。
	if !strings.Contains(ics, "DTSTART:20261102T010000Z") {
		t.Fatal("timed event must be emitted in UTC")
	}
	if !strings.HasPrefix(ics, "BEGIN:VCALENDAR\r\n") || !strings.HasSuffix(ics, "END:VCALENDAR\r\n") {
		t.Fatal("calendar must use CRLF line endings")
	}
}

func TestCalendarICSFoldsLongLines(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	long := strings.Repeat("很长的中文提醒标题", 12) // 远超 75 字节
	due := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	if _, err := createReminder(appDB, 89, SaveReminderInput{
		Title: long, DueAt: &due, RepeatRule: "none", Channels: []string{ChannelInApp},
	}); err != nil {
		t.Fatal(err)
	}

	ics, err := buildCalendarICS(appDB, 89)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.ReplaceAll(ics, "\r\n ", "") // 还原折行后应能读回完整标题
	if !strings.Contains(joined, long) {
		t.Fatal("folding must preserve the original title")
	}
	for _, line := range strings.Split(ics, "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line exceeds 75 octets: %d bytes", len(line))
		}
		if !utf8.ValidString(line) {
			t.Fatalf("folding split a UTF-8 character: %q", line)
		}
	}
}

func TestEscapeICS(t *testing.T) {
	got := escapeICS("a,b;c\\d\ne")
	want := `a\,b\;c\\d\ne`
	if got != want {
		t.Fatalf("escapeICS = %q, want %q", got, want)
	}
}

func TestCalendarFeedTokenLifecycle(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	feed, err := ensureCalendarFeed(appDB, 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Token) < 24 {
		t.Fatalf("feed token too short: %d", len(feed.Token))
	}
	// 重复调用必须返回同一个令牌，否则每次打开设置页都会让已订阅的日历失效。
	again, err := ensureCalendarFeed(appDB, 90)
	if err != nil {
		t.Fatal(err)
	}
	if again.Token != feed.Token {
		t.Fatal("ensureCalendarFeed must be idempotent")
	}
}
