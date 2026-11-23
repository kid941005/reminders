// Package core defines the stable contracts shared by the reminder domain and
// its pluggable integrations (notification channels, calendars, weather).
//
// The package intentionally has no dependency on GORM, HTTP or any concrete
// provider: business code depends on these interfaces, while adapters live in
// their own subpackages and are wired at startup. This keeps the domain layer
// testable and makes adding a new channel/calendar provider a matter of
// registering one adapter instead of editing a switch statement.
package core

import (
	"context"
	"time"
)

// OutboundMessage is a channel-neutral notification payload. Channel-specific
// adapters read the fields they need and ignore the rest.
type OutboundMessage struct {
	UserID         uint
	ReminderID     uint
	Channel        string
	Title          string
	Body           string
	IdempotencyKey string
	Targets        []uint
	// Payload carries provider-specific data (for example the hydrated
	// Reminder row) without forcing the core package to import domain models.
	Payload map[string]interface{}
}

// SendResult reports the provider-side identifier of a successful delivery.
type SendResult struct {
	ExternalID string
}

// Notifier delivers one message through a single channel.
type Notifier interface {
	Channel() string
	Send(ctx context.Context, msg OutboundMessage) (SendResult, error)
}

// CalendarEvent is the provider-neutral subset of a calendar event the app
// needs. Richer fields (attendees, alarms) are intentionally dropped: the goal
// is to mirror an event into a reminder, not to reimplement a calendar store.
type CalendarEvent struct {
	UID          string
	Summary      string
	Description  string
	Location     string
	Start        time.Time
	End          time.Time
	AllDay       bool
	Status       string
	RecurrenceID string
	RepeatRule   string
}

// CalendarProvider is an external calendar backend (CalDAV today, others may
// follow). It exposes discovery and event fetching only; persistence and
// pruning stay in the domain layer.
type CalendarProvider interface {
	Name() string
	Discover(ctx context.Context, server string) ([]string, error)
	FetchEvents(ctx context.Context, calendarURL string, from, to time.Time) ([]CalendarEvent, error)
}

// WeatherProvider is an external weather backend. Result is intentionally
// opaque so different providers can return their own DTOs; the adapter maps it
// to the domain's weather snapshot.
type WeatherProvider interface {
	Name() string
	Forecast(ctx context.Context, city string) (interface{}, error)
}
