package reminder

import (
	"context"
	"errors"
	"strconv"
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

// legacyNotifier adapts the existing channel dispatcher to core.Notifier. The
// hydrated Reminder row travels in Payload so the core contract stays free of
// GORM/domain types.
type legacyNotifier struct {
	db      *gorm.DB
	channel string
}

func (n legacyNotifier) Channel() string { return n.channel }

func (n legacyNotifier) Send(ctx context.Context, msg core.OutboundMessage) (core.SendResult, error) {
	item, ok := msg.Payload["item"].(Reminder)
	if !ok {
		return core.SendResult{}, errors.New("通知消息缺少提醒内容")
	}
	targets, _ := msg.Payload["targets"].([]uint)
	res, err := sendChannel(ctx, n.db, n.channel, msg.UserID, item, msg.IdempotencyKey, targets)
	return core.SendResult{ExternalID: res.ExternalID}, err
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
		return coreWeatherProvider{apiKey: apiKey, host: host}
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

// qweatherProvider adapts the QWeather client to core.WeatherProvider.
type coreWeatherProvider struct {
	apiKey string
	host   string
}

func (p coreWeatherProvider) Name() string { return "qweather" }

type weatherForecastSnapshot struct {
	Current qweatherCurrent
	Daily   []qweatherDay
	Alerts  []qweatherAlert
	City    qweatherLocation
}

func (p coreWeatherProvider) Forecast(ctx context.Context, city string) (interface{}, error) {
	client := newQWeatherClient(p.host, p.apiKey)
	locations, err := client.LookupCity(ctx, city)
	if err != nil {
		return nil, err
	}
	if len(locations) == 0 {
		return nil, errors.New("未找到该城市")
	}
	loc := locations[0]
	lat, err := strconv.ParseFloat(loc.Lat, 64)
	if err != nil {
		return nil, errors.New("城市坐标无效")
	}
	lon, err := strconv.ParseFloat(loc.Lon, 64)
	if err != nil {
		return nil, errors.New("城市坐标无效")
	}
	current, err := client.Current(ctx, lat, lon)
	if err != nil {
		return nil, err
	}
	daily, err := client.Daily(ctx, lat, lon, 3)
	if err != nil {
		return nil, err
	}
	alerts, _ := client.Alerts(ctx, lat, lon)
	return weatherForecastSnapshot{Current: current, Daily: daily, Alerts: alerts, City: loc}, nil
}
