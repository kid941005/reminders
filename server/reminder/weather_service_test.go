package reminder

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func weatherStubBody(path string) (string, bool) {
	today := time.Now().In(shanghai())
	forecast := func(offset int) string {
		return today.AddDate(0, 0, offset).Format("2006-01-02") + "T00:00:00+08:00"
	}
	switch {
	case strings.HasPrefix(path, "/weather/v1/current/"):
		return `{"condition":{"text":"小雨"},"temperature":{"value":15.4},"humidity":0.68,"wind":{"direction":{"compass":"se"},"scale":3},"precipitation":{"type":"rain"}}`, true
	case strings.HasPrefix(path, "/weather/v1/daily/"):
		return fmt.Sprintf(`{"days":[
			{"forecastStartTime":%q,"temperatureMax":{"value":20},"temperatureMin":{"value":12},"daytime":{"condition":{"text":"小雨"},"precipitation":{"probability":0.7,"type":"rain"}}},
			{"forecastStartTime":%q,"temperatureMax":{"value":16},"temperatureMin":{"value":10},"daytime":{"condition":{"text":"中雨"},"precipitation":{"probability":0.8,"type":"rain"}}},
			{"forecastStartTime":%q,"temperatureMax":{"value":18},"temperatureMin":{"value":9},"daytime":{"condition":{"text":"多云"},"precipitation":{"probability":0.1,"type":"none"}}},
			{"forecastStartTime":%q,"temperatureMax":{"value":19},"temperatureMin":{"value":8},"daytime":{"condition":{"text":"晴"},"precipitation":{"probability":0,"type":"none"}}}
		]}`, forecast(0), forecast(1), forecast(2), forecast(3)), true
	case strings.HasPrefix(path, "/weatheralert/v1/current/"):
		expire := today.AddDate(0, 0, 1).Format("2006-01-02") + "T20:00:00+08:00"
		return fmt.Sprintf(`{"metadata":{"zeroResult":false},"alerts":[{"id":"a1","messageType":{"code":"alert"},"eventType":{"name":"大风"},"color":{"code":"blue"},"expireTime":%q,"headline":"预计有大风"}]}`, expire), true
	default:
		return "", false
	}
}

func weatherTestClient(t *testing.T) *qweatherClient {
	t.Helper()
	client := newQWeatherClient("https://weather.test", "test-key")
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, ok := weatherStubBody(req.URL.Path)
		if !ok {
			return caldavStubResponse(http.StatusNotFound, "{}", nil), nil
		}
		return caldavStubResponse(http.StatusOK, body, map[string]string{"Content-Type": "application/json"}), nil
	})}
	return client
}

func weatherTestDay(date, condition string, min, max float64) qweatherDay {
	return qweatherDay{
		ForecastStartTime: date + "T00:00:00+08:00",
		TemperatureMin:    qweatherValue{Value: min},
		TemperatureMax:    qweatherValue{Value: max},
		Daytime: qweatherDayPart{
			Condition:     qweatherCondition{Text: condition},
			Precipitation: qweatherPrecipitation{Probability: 0, Type: "none"},
		},
	}
}

func weatherTestAlert(event, color, expire, onset, headline string) qweatherAlert {
	alert := qweatherAlert{ExpireTime: expire, OnsetTime: onset, Headline: headline}
	alert.MessageType.Code = "alert"
	alert.EventType.Name = event
	alert.Color.Code = color
	return alert
}

func TestNormalizeWeatherChannels(t *testing.T) {
	got := normalizeWeatherChannels([]string{"email", "", "email", "bogus", "inapp", "bark"})
	want := []string{ChannelEmail, ChannelInApp, ChannelBark}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("normalizeWeatherChannels = %v, want %v", got, want)
	}
	if got := normalizeWeatherChannels(nil); len(got) != 0 {
		t.Fatalf("empty channels = %v, want none", got)
	}
}

func TestWeatherConfigChannels(t *testing.T) {
	cfg := WeatherConfig{Channels: `["email","bark","email"]`}
	if got := cfg.channels(); strings.Join(got, ",") != "email,bark" {
		t.Fatalf("channels = %v", got)
	}
	cfg.Channels = "not-json"
	if got := cfg.channels(); len(got) != 0 {
		t.Fatalf("malformed channels = %v, want none", got)
	}
}

func TestDueWeatherSlot(t *testing.T) {
	loc := shanghai()
	at := func(hour, minute int) time.Time {
		return time.Date(2026, 11, 14, hour, minute, 0, 0, loc)
	}
	lastPush := func(when time.Time) *time.Time {
		value := when.UTC()
		return &value
	}
	cases := []struct {
		name string
		cfg  WeatherConfig
		now  time.Time
		want weatherSlot
		ok   bool
	}{
		{name: "before morning", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00"}, now: at(7, 59)},
		{name: "at morning", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00"}, now: at(8, 0), want: weatherSlotMorning, ok: true},
		{name: "inside morning catch-up", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00"}, now: at(11, 59), want: weatherSlotMorning, ok: true},
		{name: "after morning window", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00"}, now: at(12, 1)},
		{name: "before evening", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00", LastPushDate: "2026-11-14", LastSlot: "morning"}, now: at(19, 59)},
		{name: "at evening", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00", LastPushDate: "2026-11-14", LastSlot: "morning"}, now: at(20, 0), want: weatherSlotEvening, ok: true},
		{name: "both slots pushed", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00", LastPushDate: "2026-11-14", LastSlot: "evening"}, now: at(20, 30)},
		{name: "legacy row treats empty slot as morning", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00", LastPushDate: "2026-11-14"}, now: at(9, 0)},
		{name: "legacy row still owes evening", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00", LastPushDate: "2026-11-14"}, now: at(20, 0), want: weatherSlotEvening, ok: true},
		{name: "overlapping windows prefer evening", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "10:00"}, now: at(11, 30), want: weatherSlotEvening, ok: true},
		{name: "failed ten minutes ago", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00", LastStatus: "error", LastPushAt: lastPush(at(8, 50))}, now: at(9, 0)},
		{name: "failed thirty-one minutes ago", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00", LastStatus: "error", LastPushAt: lastPush(at(8, 29))}, now: at(9, 0), want: weatherSlotMorning, ok: true},
		{name: "manual ok does not block", cfg: WeatherConfig{MorningTime: "08:00", EveningTime: "20:00", LastStatus: "ok", LastPushAt: lastPush(at(8, 50))}, now: at(9, 0), want: weatherSlotMorning, ok: true},
		{name: "empty times default to eight and twenty", cfg: WeatherConfig{}, now: at(8, 0), want: weatherSlotMorning, ok: true},
		{name: "invalid times default to eight and twenty", cfg: WeatherConfig{MorningTime: "bad", EveningTime: "bad"}, now: at(20, 0), want: weatherSlotEvening, ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := dueWeatherSlot(tc.cfg, tc.now)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("dueWeatherSlot(%+v, %s) = (%q, %v), want (%q, %v)", tc.cfg, tc.now, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestBuildWeatherMessageMorningCoversTodayOnly(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, loc)
	daily := []qweatherDay{
		weatherTestDay("2026-11-14", "小雨", 12, 20),
		weatherTestDay("2026-11-15", "中雨", 10, 16),
		weatherTestDay("2026-11-16", "多云", 9, 18),
	}
	msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, weatherSlotMorning, daily, nil, now)
	if msg.Title != "🌧️ 今日天气 · 杭州" {
		t.Fatalf("title = %q", msg.Title)
	}
	if !strings.Contains(msg.Body, "今天小雨，12~20℃。") {
		t.Fatalf("body = %q", msg.Body)
	}
	for _, unwanted := range []string{"明天", "11-15", "11-16", "未来三天", "湿度"} {
		if strings.Contains(msg.Body, unwanted) {
			t.Fatalf("morning body should not contain %q: %s", unwanted, msg.Body)
		}
	}
}

func TestBuildWeatherMessageEveningCoversTomorrowOnly(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 20, 0, 0, 0, loc)
	daily := []qweatherDay{
		weatherTestDay("2026-11-14", "晴", 12, 20),
		weatherTestDay("2026-11-15", "中雨", 10, 16),
		weatherTestDay("2026-11-16", "多云", 9, 18),
	}
	msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, weatherSlotEvening, daily, nil, now)
	if msg.Title != "🌧️ 明日天气 · 杭州" {
		t.Fatalf("title = %q", msg.Title)
	}
	if !strings.Contains(msg.Body, "明天中雨，10~16℃。") {
		t.Fatalf("body = %q", msg.Body)
	}
	for _, unwanted := range []string{"今天", "11-16", "未来三天"} {
		if strings.Contains(msg.Body, unwanted) {
			t.Fatalf("evening body should not contain %q: %s", unwanted, msg.Body)
		}
	}
}

func TestBuildWeatherMessageEmoji(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, loc)
	cases := []struct {
		name string
		day  qweatherDay
		want string
	}{
		{name: "sunny", day: weatherTestDay("2026-11-14", "晴", 10, 20), want: "☀️"},
		{name: "cloudy", day: weatherTestDay("2026-11-14", "多云", 10, 20), want: "⛅"},
		{name: "overcast", day: weatherTestDay("2026-11-14", "阴", 10, 20), want: "☁️"},
		{name: "thunder", day: weatherTestDay("2026-11-14", "雷阵雨", 10, 20), want: "⛈️"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, weatherSlotMorning, []qweatherDay{tc.day}, nil, now)
			if !strings.HasPrefix(msg.Title, tc.want+" ") {
				t.Fatalf("title = %q, want prefix %q", msg.Title, tc.want)
			}
		})
	}

	snow := weatherTestDay("2026-11-14", "小雪", 10, 20)
	snow.Daytime.Precipitation = qweatherPrecipitation{Probability: 0.8, Type: "snow"}
	if msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, weatherSlotMorning, []qweatherDay{snow}, nil, now); !strings.HasPrefix(msg.Title, "❄️ ") {
		t.Fatalf("snow title = %q", msg.Title)
	}

	alert := weatherTestAlert("大风", "blue", "2026-11-14T20:00:00+08:00", "", "预计有大风")
	if msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, weatherSlotMorning, []qweatherDay{weatherTestDay("2026-11-14", "晴", 10, 20)}, []qweatherAlert{alert}, now); !strings.HasPrefix(msg.Title, "⚠️ ") {
		t.Fatalf("alert title = %q", msg.Title)
	}
}

func TestBuildWeatherMessageUsesNightCondition(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, loc)
	day := weatherTestDay("2026-11-14", "多云", 14, 22)
	day.Nighttime = qweatherDayPart{Condition: qweatherCondition{Text: "小雨"}, Precipitation: qweatherPrecipitation{Probability: 0.8, Type: "rain"}}
	msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, weatherSlotMorning, []qweatherDay{day}, nil, now)
	if !strings.Contains(msg.Body, "今天多云转小雨，14~22℃。") {
		t.Fatalf("body = %q", msg.Body)
	}
	if !strings.Contains(msg.Body, "降水概率 80%，记得带伞。") {
		t.Fatalf("body = %q", msg.Body)
	}
	if !strings.HasPrefix(msg.Title, "🌧️ ") {
		t.Fatalf("title = %q", msg.Title)
	}
}

func TestWeatherAlertsForSlotFiltersByTargetDay(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 20, 0, 0, 0, loc)
	alerts := []qweatherAlert{
		weatherTestAlert("大风", "blue", "2026-11-14T20:00:00+08:00", "", "今晚结束"),
		weatherTestAlert("暴雨", "red", "2026-11-16T12:00:00+08:00", "2026-11-15T08:00:00+08:00", "明天开始"),
		weatherTestAlert("寒潮", "yellow", "2026-11-16T12:00:00+08:00", "2026-11-16T08:00:00+08:00", "后天开始"),
	}
	morning := weatherAlertsForSlot(alerts, weatherSlotMorning, now, loc)
	if len(morning) != 0 {
		t.Fatalf("morning alerts = %d, want 0", len(morning))
	}
	evening := weatherAlertsForSlot(alerts, weatherSlotEvening, now, loc)
	if len(evening) != 1 || evening[0].Headline != "明天开始" {
		t.Fatalf("evening alerts = %+v", evening)
	}
}

func TestBuildWeatherMessageCapsAlertsAtTwo(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, loc)
	alerts := make([]qweatherAlert, 0, 5)
	for i := 0; i < 5; i++ {
		alerts = append(alerts, weatherTestAlert("大风", "blue", "", "", "预计有大风"))
	}
	msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, weatherSlotMorning, []qweatherDay{weatherTestDay("2026-11-14", "晴", 10, 20)}, alerts, now)
	if got := strings.Count(msg.Body, "⚠️"); got != 2 {
		t.Fatalf("alert lines = %d, want 2; body = %q", got, msg.Body)
	}
	if !strings.Contains(msg.Body, "⚠️ 大风蓝色预警：预计有大风") {
		t.Fatalf("body = %q", msg.Body)
	}
}

func TestBuildWeatherMessageMissingForecast(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, loc)
	msg := buildWeatherMessage(WeatherConfig{LocationName: strings.Repeat("杭", 100)}, weatherSlotMorning, nil, nil, now)
	if len([]rune(msg.Title)) > 80 {
		t.Fatalf("title not truncated: %d", len([]rune(msg.Title)))
	}
	if !strings.Contains(msg.Body, "今天暂无预报数据。") {
		t.Fatalf("body = %q", msg.Body)
	}
	if !strings.HasPrefix(msg.Title, "🌤️ ") {
		t.Fatalf("title = %q", msg.Title)
	}
}

func TestBuildWeatherMessageTruncatesBody(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, loc)
	alerts := make([]qweatherAlert, 0, 12)
	for i := 0; i < 12; i++ {
		alerts = append(alerts, weatherTestAlert("暴雨", "red", "", "", strings.Repeat("注意防范", 30)))
	}
	msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, weatherSlotMorning, nil, alerts, now)
	if len([]rune(msg.Body)) > 500 {
		t.Fatalf("body not truncated: %d", len([]rune(msg.Body)))
	}
}

func TestValidWeatherAlertsFiltersCancelledAndExpired(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, shanghai())
	cancelled := qweatherAlert{}
	cancelled.MessageType.Code = "cancel"
	alerts := []qweatherAlert{
		cancelled,
		{ExpireTime: "2026-11-14T07:00:00+08:00"},
		{ExpireTime: "2026-11-14T09:00:00+08:00"},
		{ExpireTime: "not-a-time"},
	}
	got := validWeatherAlerts(alerts, now)
	if len(got) != 2 {
		t.Fatalf("valid alerts = %d, want 2", len(got))
	}
}

func TestSaveWeatherConfigValidation(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	lat, lon := 30.27, 120.15
	badLat, badLon := 91.0, 181.0
	cases := []struct {
		name string
		in   WeatherConfigInput
	}{
		{name: "empty name", in: WeatherConfigInput{Latitude: &lat, Longitude: &lon, MorningTime: "08:00", EveningTime: "20:00"}},
		{name: "missing latitude", in: WeatherConfigInput{LocationName: "杭州", Longitude: &lon}},
		{name: "bad latitude", in: WeatherConfigInput{LocationName: "杭州", Latitude: &badLat, Longitude: &lon}},
		{name: "bad longitude", in: WeatherConfigInput{LocationName: "杭州", Latitude: &lat, Longitude: &badLon}},
		{name: "bad morning", in: WeatherConfigInput{LocationName: "杭州", Latitude: &lat, Longitude: &lon, MorningTime: "8点", EveningTime: "20:00"}},
		{name: "bad evening", in: WeatherConfigInput{LocationName: "杭州", Latitude: &lat, Longitude: &lon, MorningTime: "08:00", EveningTime: "8点"}},
		{name: "same slots", in: WeatherConfigInput{LocationName: "杭州", Latitude: &lat, Longitude: &lon, MorningTime: "08:00", EveningTime: "08:00"}},
		{name: "morning after evening", in: WeatherConfigInput{LocationName: "杭州", Latitude: &lat, Longitude: &lon, MorningTime: "21:00", EveningTime: "20:00"}},
		{name: "no channels", in: WeatherConfigInput{LocationName: "杭州", Latitude: &lat, Longitude: &lon, MorningTime: "08:00", EveningTime: "20:00"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := SaveWeatherConfig(appDB, 1, tc.in); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPushWeatherWritesInAppNotification(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	t.Setenv("QWEATHER_API_HOST", "https://weather.test")
	t.Setenv("QWEATHER_API_KEY", "test-key")
	originalFactory := weatherClientFactory
	weatherClientFactory = func(string, string) *qweatherClient { return weatherTestClient(t) }
	t.Cleanup(func() { weatherClientFactory = originalFactory })

	localNow := time.Now().In(shanghai())
	cfg := WeatherConfig{
		UserID: 7, Enabled: true, LocationName: "杭州",
		Latitude: 30.27, Longitude: 120.15,
		MorningTime: localNow.Add(-time.Minute).Format("15:04"),
		EveningTime: localNow.Add(2 * time.Hour).Format("15:04"),
		Channels:    `["inapp"]`,
	}
	if err := appDB.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}

	msg, err := pushWeather(appDB, cfg, false, weatherSlotMorning)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Title, "杭州") || !strings.Contains(msg.Title, "今日天气") {
		t.Fatalf("title = %q", msg.Title)
	}
	if !strings.Contains(msg.Body, "今天") || strings.Contains(msg.Body, "明天") {
		t.Fatalf("body = %q", msg.Body)
	}

	var notification Notification
	if err := appDB.Where("user_id = ? AND type = ?", 7, "weather").First(&notification).Error; err != nil {
		t.Fatal(err)
	}
	if notification.Title != msg.Title || notification.Body != msg.Body {
		t.Fatalf("notification = %+v, message = %+v", notification, msg)
	}

	var saved WeatherConfig
	if err := appDB.First(&saved, cfg.ID).Error; err != nil {
		t.Fatal(err)
	}
	today := time.Now().In(shanghai()).Format("2006-01-02")
	if saved.LastPushDate != today || saved.LastSlot != string(weatherSlotMorning) || saved.LastStatus != "ok" || saved.LastPushAt == nil {
		t.Fatalf("saved = %+v", saved)
	}

	// A manual push must not consume the scheduled slot.
	before := saved.LastPushDate
	if _, err := pushWeather(appDB, saved, true, weatherSlotEvening); err != nil {
		t.Fatal(err)
	}
	var after WeatherConfig
	if err := appDB.First(&after, cfg.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.LastPushDate != before || after.LastSlot != string(weatherSlotMorning) {
		t.Fatalf("manual push changed slot state: %+v", after)
	}
}

func TestWeatherConfigDTOUnconfigured(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()
	t.Setenv("QWEATHER_API_HOST", "")
	t.Setenv("QWEATHER_API_KEY", "")

	dto := weatherConfigDTO(appDB, WeatherConfig{}, false)
	if dto.Configured || dto.AdminConfigured {
		t.Fatalf("unexpected configured state: %+v", dto)
	}
	if dto.MorningTime != "08:00" || dto.EveningTime != "20:00" || len(dto.Channels) != 0 {
		t.Fatalf("defaults = %+v", dto)
	}
}

func TestSaveWeatherConfigPersistsDisabledState(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()
	db := appDB
	t.Setenv("QWEATHER_API_HOST", "https://weather.test")
	t.Setenv("QWEATHER_API_KEY", "test-key")
	originalFactory := weatherClientFactory
	weatherClientFactory = func(string, string) *qweatherClient { return weatherTestClient(t) }
	t.Cleanup(func() { weatherClientFactory = originalFactory })

	lat, lon := 30.27, 120.15
	disabled := false
	cfg, err := SaveWeatherConfig(db, 9, WeatherConfigInput{
		LocationName: "杭州", Latitude: &lat, Longitude: &lon,
		MorningTime: "08:00", EveningTime: "20:00", Channels: []string{"inapp"}, Enabled: &disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Fatalf("returned config should be disabled: %+v", cfg)
	}
	stored, ok := getWeatherConfig(db, 9)
	if !ok {
		t.Fatal("config not stored")
	}
	if stored.Enabled {
		t.Fatalf("stored config should stay disabled: %+v", stored)
	}
	if stored.EveningTime != "20:00" {
		t.Fatalf("evening time = %q", stored.EveningTime)
	}
}

func TestBuildWeatherMessageAcceptsPercentUnits(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, shanghai())
	day := weatherTestDay("2026-11-14", "小雨", 12, 20)
	day.Daytime.Precipitation = qweatherPrecipitation{Probability: 70, Type: "rain"}
	msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, weatherSlotMorning, []qweatherDay{day}, nil, now)
	if !strings.Contains(msg.Body, "降水概率 70%，记得带伞。") {
		t.Fatalf("probability should stay 70%%: %s", msg.Body)
	}
}

func TestBuildWeatherMessageParsesMinutePrecisionTimestamps(t *testing.T) {
	now := time.Date(2026, 11, 14, 20, 0, 0, 0, shanghai())
	daily := []qweatherDay{
		weatherTestDay("2026-11-14", "晴", 12, 20),
		{
			ForecastStartTime: "2026-11-15T00:00+08:00",
			TemperatureMin:    qweatherValue{Value: 10},
			TemperatureMax:    qweatherValue{Value: 16},
			Daytime:           qweatherDayPart{Condition: qweatherCondition{Text: "小雨"}, Precipitation: qweatherPrecipitation{Probability: 0.8, Type: "rain"}},
		},
	}
	expired := weatherTestAlert("大风", "blue", "2026-11-13T20:00+08:00", "", "已过期的大风")
	msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, weatherSlotEvening, daily, []qweatherAlert{expired}, now)
	if !strings.Contains(msg.Body, "明天小雨，10~16℃。") {
		t.Fatalf("minute-precision forecast should be parsed: %s", msg.Body)
	}
	if strings.Contains(msg.Body, "已过期的大风") {
		t.Fatalf("expired alert should be filtered: %s", msg.Body)
	}
}
