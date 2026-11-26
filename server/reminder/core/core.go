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

// OutboundMessage is a channel-neutral, fully rendered notification. The
// domain layer fills in every field an adapter may need, so adapters never have
// to type-assert an opaque map or reach back into domain models. Targets is the
// already-resolved list of binding IDs for multi-target channels such as email.
type OutboundMessage struct {
	UserID           uint
	ReminderID       uint
	Channel          string
	Title            string
	Body             string
	Notes            string
	Subject          string
	Group            string
	Kind             string
	NotificationType string
	DueAt            *time.Time
	IdempotencyKey   string
	Targets          []uint
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

// WeatherProvider and its DTOs live in weather.go.
