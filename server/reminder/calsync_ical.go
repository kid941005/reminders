package reminder

import (
	"time"

	"smallgo/server/reminder/calendar"
)

// ICalEvent is kept as an alias so existing service/tests keep their local
// vocabulary while the parser itself lives in the calendar subpackage.
type ICalEvent = calendar.Event

// ParseICalEvents delegates to the calendar package.
func ParseICalEvents(data string, loc *time.Location) []ICalEvent {
	return calendar.ParseEvents(data, loc)
}
