package reminder

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"smallgo/server/lunar"
	"smallgo/server/response"
	"smallgo/server/sysconfig"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// icsExpandLimit 是农历重复与农历纪念日展开的次数上限。
// RRULE 无法表达「农历每年」，只能把未来若干次展开成独立事件；
// 取 10 次覆盖十年，日历客户端每次刷新都会重新拉取，足够用且不会撑大文件。
const icsExpandLimit = 10

func newFeedToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ensureCalendarFeed 取用户的订阅令牌，不存在则创建。
func ensureCalendarFeed(db *gorm.DB, userID uint) (CalendarFeed, error) {
	var feed CalendarFeed
	err := db.Where("user_id = ?", userID).First(&feed).Error
	if err == nil {
		return feed, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return CalendarFeed{}, err
	}
	token, err := newFeedToken()
	if err != nil {
		return CalendarFeed{}, err
	}
	feed = CalendarFeed{UserID: userID, Token: token}
	if err := db.Create(&feed).Error; err != nil {
		return CalendarFeed{}, err
	}
	return feed, nil
}

// feedBaseURL 取当前请求的外部访问地址。自托管常挂在反向代理后面，
// 所以优先采用 X-Forwarded-* 头，否则日历里会写进内网地址。
func feedBaseURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := c.Request.Host
	if forwarded := c.GetHeader("X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host
}

func feedURL(c *gin.Context, token string) string {
	return feedBaseURL(c) + "/api/reminder/feed.ics?token=" + token
}

func handleGetCalendarFeed(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		feed, err := ensureCalendarFeed(db, currentUserID(c))
		if err != nil {
			response.ErrorInternal(c, "生成订阅地址失败")
			return
		}
		response.Success(c, gin.H{
			"url":        feedURL(c, feed.Token),
			"created_at": feed.CreatedAt,
		})
	}
}

func handleResetCalendarFeed(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := currentUserID(c)
		if _, err := ensureCalendarFeed(db, userID); err != nil {
			response.ErrorInternal(c, "生成订阅地址失败")
			return
		}
		token, err := newFeedToken()
		if err != nil {
			response.ErrorInternal(c, "生成订阅地址失败")
			return
		}
		if err := db.Model(&CalendarFeed{}).Where("user_id = ?", userID).
			Update("token", token).Error; err != nil {
			response.ErrorInternal(c, "重置订阅地址失败")
			return
		}
		response.Success(c, gin.H{"url": feedURL(c, token)})
	}
}

// handleCalendarFeed 是免登录的订阅入口：日历客户端无法携带 JWT，
// 只能凭 URL 里的高熵令牌鉴权，令牌无效一律按不存在处理。
func handleCalendarFeed(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(c.Query("token"))
		if token == "" {
			c.Status(http.StatusNotFound)
			return
		}
		var feed CalendarFeed
		if err := db.Where("token = ?", token).First(&feed).Error; err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		body, err := buildCalendarICS(db, feed.UserID)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Header("Content-Type", "text/calendar; charset=utf-8")
		c.Header("Content-Disposition", `inline; filename="reminders.ics"`)
		c.Header("Cache-Control", "no-cache")
		c.String(http.StatusOK, body)
	}
}

// buildCalendarICS 把未完成的提醒与纪念日组装成一份 iCalendar。
//
// 只输出未完成的提醒：已完成的放进日历会让「过去那天做了几件事」变成噪音，
// 而历来的用途是「接下来要做什么」。全天事件用 DATE 值，且 DTEND 取次日
// ——ICS 的全天事件是左闭右开区间。
func buildCalendarICS(db *gorm.DB, userID uint) (string, error) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now()
	var reminders []Reminder
	if err := db.Where("user_id = ? AND deleted_at IS NULL AND completed_at IS NULL AND due_at IS NOT NULL", userID).
		Order("due_at ASC").Find(&reminders).Error; err != nil {
		return "", err
	}
	var countdowns []Countdown
	if err := db.Where("user_id = ?", userID).Order("target_date ASC").Find(&countdowns).Error; err != nil {
		return "", err
	}

	var b strings.Builder
	writeICSLine(&b, "BEGIN:VCALENDAR")
	writeICSLine(&b, "VERSION:2.0")
	writeICSLine(&b, "PRODID:-//kid941005 reminders//CN")
	writeICSLine(&b, "CALSCALE:GREGORIAN")
	writeICSLine(&b, "METHOD:PUBLISH")
	writeICSLine(&b, "X-WR-CALNAME:"+escapeICS("提醒事项"))
	writeICSLine(&b, "X-WR-TIMEZONE:Asia/Shanghai")

	for _, item := range reminders {
		writeReminderEvents(&b, item, loc, now)
	}
	for _, item := range countdowns {
		writeCountdownEvents(&b, item, loc, now)
	}
	// 节日默认不写入：订阅的主体是「我的事」，节日应当显式开启，
	// 否则日历会被一堆与自己无关的条目占满。
	if include, err := sysconfig.GetConfig(db, "ics_include_holidays", userID); err == nil && include == "true" {
		writeHolidayEvents(&b, loc, now)
	}

	writeICSLine(&b, "END:VCALENDAR")
	return b.String(), nil
}

func writeReminderEvents(b *strings.Builder, item Reminder, loc *time.Location, now time.Time) {
	if item.DueAt == nil {
		return
	}
	lunarRepeat := item.Calendar == "lunar" && (item.RepeatRule == "monthly" || item.RepeatRule == "yearly")
	if lunarRepeat {
		// 农历重复无法用 RRULE 表达，展开成若干独立事件。
		cursor := item.DueAt.In(loc)
		for i := 0; i < icsExpandLimit; i++ {
			writeReminderEvent(b, item, cursor, loc, now, fmt.Sprintf("reminder-%d-%d@kid941005-reminders", item.ID, i), "")
			next, err := nextOccurrenceWithCalendar(cursor, item.RepeatRule, item.Calendar, item.LunarAnchor)
			if err != nil || !next.After(cursor) {
				break
			}
			cursor = next
		}
		return
	}
	writeReminderEvent(b, item, item.DueAt.In(loc), loc, now,
		fmt.Sprintf("reminder-%d@kid941005-reminders", item.ID), icsRRule(item))
}

func writeReminderEvent(b *strings.Builder, item Reminder, due time.Time, loc *time.Location,
	now time.Time, uid, rrule string) {
	writeICSLine(b, "BEGIN:VEVENT")
	writeICSLine(b, "UID:"+uid)
	writeICSLine(b, "DTSTAMP:"+now.UTC().Format("20060102T150405Z"))
	if item.AllDay {
		start := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, loc)
		writeICSLine(b, "DTSTART;VALUE=DATE:"+start.Format("20060102"))
		writeICSLine(b, "DTEND;VALUE=DATE:"+start.AddDate(0, 0, 1).Format("20060102"))
	} else {
		// 统一用 UTC 输出，省掉 VTIMEZONE 定义；30 分钟是提醒类事件的合理默认时长。
		start := due.UTC()
		writeICSLine(b, "DTSTART:"+start.Format("20060102T150405Z"))
		writeICSLine(b, "DTEND:"+start.Add(30*time.Minute).Format("20060102T150405Z"))
	}
	if rrule != "" {
		writeICSLine(b, "RRULE:"+rrule)
	}
	writeICSLine(b, "SUMMARY:"+escapeICS(item.Title))
	if notes := strings.TrimSpace(item.Notes); notes != "" {
		writeICSLine(b, "DESCRIPTION:"+escapeICS(notes))
	}
	writeICSLine(b, "TRANSP:OPAQUE")
	writeICSLine(b, "END:VEVENT")
}

// writeHolidayEvents 追加「当年 + 次年」的节日全天事件。
//
// 只生成节日，不生成二十四节气：节气一年 24 个，全部塞进日历会把真正的事项挤掉。
// 农历节日每年对应的公历日期不同，因此按年逐月展开，而不是用 RRULE。
func writeHolidayEvents(b *strings.Builder, loc *time.Location, now time.Time) {
	year := now.In(loc).Year()
	for y := year; y <= year+1; y++ {
		for m := 1; m <= 12; m++ {
			days, err := lunar.CalendarMonth(y, time.Month(m), loc)
			if err != nil {
				continue
			}
			for _, day := range days {
				// 月视图返回的网格含相邻月份补格，只看本月，避免同一天写两次。
				if !day.InMonth || day.Festival == "" {
					continue
				}
				start := time.Date(day.Year, time.Month(day.Month), day.Day, 0, 0, 0, 0, loc)
				writeICSLine(b, "BEGIN:VEVENT")
				writeICSLine(b, fmt.Sprintf("UID:holiday-%04d%02d%02d@kid941005-reminders", day.Year, day.Month, day.Day))
				writeICSLine(b, "DTSTAMP:"+now.UTC().Format("20060102T150405Z"))
				writeICSLine(b, "DTSTART;VALUE=DATE:"+start.Format("20060102"))
				writeICSLine(b, "DTEND;VALUE=DATE:"+start.AddDate(0, 0, 1).Format("20060102"))
				writeICSLine(b, "SUMMARY:"+escapeICS(day.Festival))
				writeICSLine(b, "TRANSP:TRANSPARENT")
				writeICSLine(b, "END:VEVENT")
			}
		}
	}
}

func writeCountdownEvents(b *strings.Builder, item Countdown, loc *time.Location, now time.Time) {
	uid := fmt.Sprintf("countdown-%d@kid941005-reminders", item.ID)
	if item.RepeatYearly && item.Calendar == "lunar" {
		anchor, ok := parseLunarAnchor(item.LunarAnchor)
		if !ok {
			return
		}
		cursor := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc).Add(-time.Second)
		for i := 0; i < icsExpandLimit; i++ {
			occurrence, err := lunar.NextOccurrence(anchor, cursor, loc)
			if err != nil {
				break
			}
			writeCountdownEvent(b, item, occurrence, loc, now,
				fmt.Sprintf("countdown-%d-%d@kid941005-reminders", item.ID, i), "")
			cursor = occurrence.Add(time.Hour)
		}
		return
	}
	rrule := ""
	if item.RepeatYearly {
		rrule = "FREQ=YEARLY"
	}
	writeCountdownEvent(b, item, item.TargetDate.In(loc), loc, now, uid, rrule)
}

func writeCountdownEvent(b *strings.Builder, item Countdown, date time.Time, loc *time.Location,
	now time.Time, uid, rrule string) {
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	writeICSLine(b, "BEGIN:VEVENT")
	writeICSLine(b, "UID:"+uid)
	writeICSLine(b, "DTSTAMP:"+now.UTC().Format("20060102T150405Z"))
	writeICSLine(b, "DTSTART;VALUE=DATE:"+start.Format("20060102"))
	writeICSLine(b, "DTEND;VALUE=DATE:"+start.AddDate(0, 0, 1).Format("20060102"))
	if rrule != "" {
		writeICSLine(b, "RRULE:"+rrule)
	}
	writeICSLine(b, "SUMMARY:"+escapeICS(item.Title))
	if notes := strings.TrimSpace(item.Notes); notes != "" {
		writeICSLine(b, "DESCRIPTION:"+escapeICS(notes))
	}
	writeICSLine(b, "TRANSP:TRANSPARENT")
	writeICSLine(b, "END:VEVENT")
}

// icsRRule 把项目的简单重复规则映射成 RRULE。模型里没有「间隔」字段，
// 所以一律 INTERVAL=1；农历重复不走这里，由调用方展开成独立事件。
func icsRRule(item Reminder) string {
	switch item.RepeatRule {
	case "daily":
		return "FREQ=DAILY"
	case "weekly":
		return "FREQ=WEEKLY"
	case "monthly":
		return "FREQ=MONTHLY"
	case "yearly":
		return "FREQ=YEARLY"
	}
	return ""
}

// escapeICS 按 RFC 5545 转义文本值：反斜杠、分号、逗号与换行。
func escapeICS(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, ";", `\;`)
	value = strings.ReplaceAll(value, ",", `\,`)
	value = strings.ReplaceAll(value, "\r\n", `\n`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return value
}

// foldICSLine 把超过 75 字节的行折成多行（续行以单个空格开头）。
// 必须在 UTF-8 字符边界上切分，否则中文会被截成半个字符，客户端直接解析失败。
func foldICSLine(line string) string {
	const limit = 75
	if len(line) <= limit {
		return line
	}
	var out strings.Builder
	chunk := line
	first := true
	for {
		width := limit
		if !first {
			width = limit - 1 // 续行已占用一个前导空格
		}
		if len(chunk) <= width {
			out.WriteString(chunk)
			return out.String()
		}
		cut := width
		for cut > 0 && !utf8.RuneStart(chunk[cut]) {
			cut--
		}
		if cut == 0 {
			cut = width
		}
		out.WriteString(chunk[:cut])
		out.WriteString("\r\n ")
		chunk = chunk[cut:]
		first = false
	}
}

func writeICSLine(b *strings.Builder, line string) {
	b.WriteString(foldICSLine(line))
	b.WriteString("\r\n")
}
