package reminder

import (
	"context"
	"errors"
	"time"

	"smallgo/server/reminder/calendar"
	"smallgo/server/reminder/channels"
	"smallgo/server/reminder/core"
	"smallgo/server/reminder/weather"

	"gorm.io/gorm"
)

// Integration registries are populated once at startup. Keeping them here (in
// the composition root) means core/ has no concrete dependency and adapters can
// be replaced in tests.
var (
	notifierRegistry = channels.NewRegistry()
	calendarRegistry = calendar.NewRegistry()
	weatherRegistry  = weather.NewRegistry()
	dispatcher       = channels.NewDispatcher(notifierRegistry)
)

// legacyNotifier adapts the existing channel delivery path to core.Notifier.
// The core message is already fully rendered, so the adapter only maps it onto
// the internal outbound representation and delegates delivery.
type legacyNotifier struct {
	db      *gorm.DB
	channel string
}

func (n legacyNotifier) Channel() string { return n.channel }

func (n legacyNotifier) Send(ctx context.Context, msg core.OutboundMessage) (core.SendResult, error) {
	out := outboundFromCore(msg)
	if out.Kind == "" {
		return core.SendResult{}, errors.New("通知消息缺少提醒内容")
	}
	res, err := deliverMessage(ctx, n.db, n.channel, msg.UserID, out, msg.IdempotencyKey, msg.Targets)
	return core.SendResult{ExternalID: res.ExternalID}, err
}

// outboundFromCore expands the provider-neutral message into the internal
// delivery struct shared by every channel implementation.
func outboundFromCore(msg core.OutboundMessage) outboundMessage {
	return outboundMessage{
		Title: msg.Title, Body: msg.Body, Notes: msg.Notes, Subject: msg.Subject,
		Group: msg.Group, Kind: msg.Kind, NotificationType: msg.NotificationType,
		ReminderID: reminderIDPtr(msg.ReminderID), DueAt: msg.DueAt,
	}
}

func reminderIDPtr(id uint) *uint {
	if id == 0 {
		return nil
	}
	return &id
}

func registerBuiltinIntegrations() {
	for _, channel := range []string{
		ChannelInApp, ChannelEmail, ChannelSMS, ChannelFeishu,
		ChannelQQ, ChannelDingTalk, ChannelBark,
	} {
		ch := channel
		notifierRegistry.Register(ch, func(db *gorm.DB) core.Notifier {
			return legacyNotifier{db: db, channel: ch}
		})
	}
	calendarRegistry.Register("caldav", func(username, password string) core.CalendarProvider {
		return caldavProvider{client: newCalDAVClient(username, password)}
	})
	weatherRegistry.Register("qweather", func(apiKey, host string) core.WeatherProvider {
		return newQWeatherClient(host, apiKey)
	})
}

// caldavProvider adapts caldavClient to core.CalendarProvider.
type caldavProvider struct {
	client *caldavClient
}

func (p caldavProvider) Name() string { return "caldav" }

func (p caldavProvider) Discover(ctx context.Context, server string) ([]string, error) {
	return p.client.Discover(ctx, server)
}

func (p caldavProvider) FetchEvents(ctx context.Context, calendarURL string, from, to time.Time) ([]core.CalendarEvent, error) {
	events, err := p.client.FetchEvents(ctx, calendarURL, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]core.CalendarEvent, 0, len(events))
	for _, e := range events {
		out = append(out, core.CalendarEvent{
			UID: e.UID, Summary: e.Summary, Description: e.Description, Location: e.Location,
			Start: e.Start, End: e.End, AllDay: e.AllDay, Status: e.Status,
			RecurrenceID: e.RecurrenceID, RepeatRule: e.RepeatRule,
		})
	}
	return out, nil
}
