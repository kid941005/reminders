package reminder

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"smallgo/server/lunar"
	"smallgo/server/sysconfig"

	"gorm.io/gorm"
)

func allDayTimeConfig(tx *gorm.DB, userID uint) string {
	fallback := "09:00"
	if def, ok := sysconfig.GetConfigDef("reminder_all_day_time"); ok && strings.TrimSpace(def.Default) != "" {
		fallback = def.Default
	}
	raw, err := sysconfig.GetConfig(tx, "reminder_all_day_time", userID)
	if err != nil || strings.TrimSpace(raw) == "" {
		return fallback
	}
	if _, err := time.Parse("15:04", strings.TrimSpace(raw)); err != nil {
		return fallback
	}
	return strings.TrimSpace(raw)
}

// allDayRunAt converts an all-day reminder's date (stored as UTC midnight of
// the local date it represents) into the configured local delivery instant.
func allDayRunAt(due time.Time, hhmm string) time.Time {
	loc := shanghai()
	local := due.In(loc)
	hour, minute := 9, 0
	if parsed, err := time.Parse("15:04", strings.TrimSpace(hhmm)); err == nil {
		hour, minute = parsed.Hour(), parsed.Minute()
	}
	return time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc).UTC()
}

// nextOccurrenceWithCalendar dispatches to the lunar calendar when the
// reminder follows it, and to the plain Gregorian rules otherwise. Lunar
// recurrence never fails hard here: if the anchor is missing or unreadable
// the reminder simply keeps its current due date, which leaves the delivery
// jobs untouched.
func nextOccurrenceWithCalendar(t time.Time, rule, calendar, anchor string) (time.Time, error) {
	if calendar == "lunar" {
		parsed, ok := parseLunarAnchor(anchor)
		if !ok {
			return t, nil
		}
		loc := shanghai()
		switch rule {
		case "monthly":
			return lunar.NextMonthlyOccurrence(parsed.Day, t, loc)
		case "yearly":
			return lunar.NextOccurrence(parsed, t, loc)
		}
	}
	return nextOccurrence(t, rule), nil
}

// parseLunarAnchor decodes the stored "M:D" lunar anchor. A missing colon,
// out-of-range month, or non-positive day means the anchor was never stored
// or is corrupt; the caller falls back to keeping the current due date.
func parseLunarAnchor(anchor string) (lunar.Date, bool) {
	monthStr, dayStr, found := strings.Cut(anchor, ":")
	if !found {
		return lunar.Date{}, false
	}
	month, errMonth := strconv.Atoi(monthStr)
	day, errDay := strconv.Atoi(dayStr)
	if errMonth != nil || errDay != nil {
		return lunar.Date{}, false
	}
	absMonth := month
	if absMonth < 0 {
		absMonth = -absMonth
	}
	if absMonth < 1 || absMonth > 12 || day < 1 || day > 30 {
		return lunar.Date{}, false
	}
	return lunar.Date{Year: 0, Month: month, Day: day}, true
}

// normalizeLunarAnchor validates a client-provided lunar anchor or derives
// one from the reminder's due date when the client did not supply one.
func normalizeLunarAnchor(anchor string, due time.Time) (string, error) {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		loc := shanghai()
		converted := lunar.FromTime(due.In(loc))
		return fmt.Sprintf("%d:%d", converted.Month, converted.Day), nil
	}
	parsed, ok := parseLunarAnchor(anchor)
	if !ok {
		return "", errors.New("农历锚点格式无效")
	}
	return fmt.Sprintf("%d:%d", parsed.Month, parsed.Day), nil
}

func nextOccurrence(t time.Time, rule string) time.Time {
	switch rule {
	case "daily":
		return t.AddDate(0, 0, 1)
	case "weekly":
		return t.AddDate(0, 0, 7)
	case "monthly":
		day := t.Day()
		firstNext := time.Date(t.Year(), t.Month()+1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
		lastDay := time.Date(firstNext.Year(), firstNext.Month()+1, 0, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()).Day()
		if day > lastDay {
			day = lastDay
		}
		return time.Date(firstNext.Year(), firstNext.Month(), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	case "yearly":
		year := t.Year() + 1
		day := t.Day()
		lastDay := time.Date(year, t.Month()+1, 0, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()).Day()
		if day > lastDay {
			day = lastDay
		}
		return time.Date(year, t.Month(), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	default:
		return t
	}
}
