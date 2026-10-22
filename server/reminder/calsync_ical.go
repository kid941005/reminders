package reminder

import (
	"strings"
	"time"
)

// ICalEvent is the subset of a VEVENT this app needs. Anything richer (attendees,
// alarms, exceptions) is intentionally dropped: the goal is to mirror the event
// into a reminder, not to reimplement a calendar store.
type ICalEvent struct {
	UID         string
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	AllDay      bool
	// Status carries STATUS:CANCELLED so cancelled events can be skipped instead
	// of imported as live reminders.
	Status string
	// RepeatRule is already mapped onto this app's vocabulary
	// (none/daily/weekly/monthly/yearly).
	RepeatRule string
}

// ParseICalEvents extracts VEVENTs from an iCalendar payload.
//
// The parser is deliberately forgiving: unknown properties and parameters are
// ignored, and a malformed event is skipped rather than failing the whole sync.
// loc is used for DTSTART values without an explicit zone — DingTalk renders in
// the account's local time, so treating those as Asia/Shanghai matches what the
// user sees in the DingTalk client.
func ParseICalEvents(data string, loc *time.Location) []ICalEvent {
	if loc == nil {
		loc = time.UTC
	}
	lines := unfoldICalLines(data)

	var events []ICalEvent
	var current *ICalEvent
	inEvent := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch strings.ToUpper(trimmed) {
		case "BEGIN:VEVENT":
			inEvent = true
			current = &ICalEvent{RepeatRule: "none"}
			continue
		case "END:VEVENT":
			// 没有 DTSTART 的事件无法提醒，直接丢弃。
			if inEvent && current != nil && !current.Start.IsZero() {
				events = append(events, *current)
			}
			inEvent = false
			current = nil
			continue
		}
		if !inEvent || current == nil {
			continue
		}

		name, params, value := splitICalProperty(trimmed)
		switch name {
		case "UID":
			current.UID = unescapeICalText(value)
		case "SUMMARY":
			current.Summary = unescapeICalText(value)
		case "DESCRIPTION":
			current.Description = unescapeICalText(value)
		case "LOCATION":
			current.Location = unescapeICalText(value)
		case "STATUS":
			current.Status = strings.ToUpper(value)
		case "DTSTART":
			if start, allDay, ok := parseICalTime(value, params, loc); ok {
				current.Start = start
				current.AllDay = allDay
			}
		case "DTEND":
			if end, _, ok := parseICalTime(value, params, loc); ok {
				current.End = end
			}
		case "RRULE":
			current.RepeatRule = mapRRuleFreq(value)
		}
		// 事件数量上限：损坏或恶意的数据不应该把整个进程拖垮。
		if len(events) >= 5000 {
			return events
		}
	}
	return events
}

// unfoldICalLines joins continuation lines. RFC 5545 folds long lines with CRLF
// followed by a single space or tab; without unfolding, every folded property is
// truncated at the fold point.
func unfoldICalLines(data string) []string {
	raw := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// splitICalProperty separates "NAME;PARAM=VAL:value" into its three parts.
// The colon inside a quoted parameter value must not be treated as the
// separator, so scanning is quote-aware.
func splitICalProperty(line string) (name string, params map[string]string, value string) {
	params = map[string]string{}
	inQuotes := false
	colonAt := -1
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuotes = !inQuotes
		case ':':
			if !inQuotes {
				colonAt = i
			}
		}
		if colonAt >= 0 {
			break
		}
	}
	if colonAt < 0 {
		return "", params, ""
	}
	head := line[:colonAt]
	value = line[colonAt+1:]

	parts := strings.Split(head, ";")
	name = strings.ToUpper(parts[0])
	for _, part := range parts[1:] {
		key, val, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		params[strings.ToUpper(key)] = strings.Trim(val, `"`)
	}
	return name, params, value
}

// parseICalTime handles the three shapes a DTSTART can take: a bare date
// (VALUE=DATE), a UTC timestamp ending in Z, and a local timestamp optionally
// qualified by TZID.
func parseICalTime(value string, params map[string]string, loc *time.Location) (time.Time, bool, bool) {
	value = strings.TrimSpace(value)
	zone := loc
	if tzid := params["TZID"]; tzid != "" {
		if loaded, err := time.LoadLocation(tzid); err == nil {
			zone = loaded
		}
	}

	if strings.EqualFold(params["VALUE"], "DATE") || len(value) == 8 {
		if parsed, err := time.ParseInLocation("20060102", value, zone); err == nil {
			return parsed, true, true
		}
	}
	if strings.HasSuffix(value, "Z") {
		if parsed, err := time.Parse("20060102T150405Z", value); err == nil {
			return parsed, false, true
		}
	}
	layouts := []string{"20060102T150405", "20060102T1504"}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, value, zone); err == nil {
			return parsed, false, true
		}
	}
	return time.Time{}, false, false
}

// mapRRuleFreq keeps only the frequency. COUNT/UNTIL/INTERVAL are dropped: this
// app's repeat rules have no such knobs, and importing a truncated series is
// less surprising than silently inventing an end date.
func mapRRuleFreq(value string) string {
	for _, part := range strings.Split(strings.ToUpper(value), ";") {
		freq, found := strings.CutPrefix(part, "FREQ=")
		if !found {
			continue
		}
		switch freq {
		case "DAILY":
			return "daily"
		case "WEEKLY":
			return "weekly"
		case "MONTHLY":
			return "monthly"
		case "YEARLY":
			return "yearly"
		}
	}
	return "none"
}

// unescapeICalText decodes the escapes RFC 5545 allows in text values.
func unescapeICalText(value string) string {
	if !strings.Contains(value, `\`) {
		return value
	}
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' || i+1 >= len(value) {
			b.WriteByte(value[i])
			continue
		}
		i++
		switch value[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		case ',':
			b.WriteByte(',')
		case ';':
			b.WriteByte(';')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte(value[i])
		}
	}
	return b.String()
}
