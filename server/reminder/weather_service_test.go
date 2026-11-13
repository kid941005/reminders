package reminder

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func weatherStubBody(path string) (string, bool) {
	switch {
	case strings.HasPrefix(path, "/weather/v1/current/"):
		return `{"condition":{"text":"小雨"},"temperature":{"value":15.4},"humidity":0.68,"wind":{"direction":{"compass":"se"},"scale":3},"precipitation":{"type":"rain"}}`, true
	case strings.HasPrefix(path, "/weather/v1/daily/"):
		return `{"days":[
			{"forecastStartTime":"2026-11-14T00:00:00+08:00","temperatureMax":{"value":20},"temperatureMin":{"value":12},"daytime":{"condition":{"text":"小雨"},"precipitation":{"probability":0.7,"type":"rain"}}},
			{"forecastStartTime":"2026-11-15T00:00:00+08:00","temperatureMax":{"value":16},"temperatureMin":{"value":10},"daytime":{"condition":{"text":"中雨"},"precipitation":{"probability":0.8,"type":"rain"}}},
			{"forecastStartTime":"2026-11-16T00:00:00+08:00","temperatureMax":{"value":18},"temperatureMin":{"value":9},"daytime":{"condition":{"text":"多云"},"precipitation":{"probability":0.1,"type":"none"}}},
			{"forecastStartTime":"2026-11-17T00:00:00+08:00","temperatureMax":{"value":19},"temperatureMin":{"value":8},"daytime":{"condition":{"text":"晴"},"precipitation":{"probability":0,"type":"none"}}}
		]}`, true
	case strings.HasPrefix(path, "/weatheralert/v1/current/"):
		return `{"metadata":{"zeroResult":false},"alerts":[{"id":"a1","messageType":{"code":"alert"},"eventType":{"name":"大风"},"color":{"code":"blue"},"expireTime":"2026-11-14T20:00:00+08:00","headline":"预计有大风"}]}`, true
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

func TestNormalizeWeatherChannels(t *testing.T) {
	got := normalizeWeatherChannels([]string{"email", "", "email", "bogus", "inapp", "bark"})
	want := []string{ChannelInApp, ChannelEmail, ChannelBark}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("normalizeWeatherChannels = %v, want %v", got, want)
	}
	if got := normalizeWeatherChannels(nil); len(got) != 1 || got[0] != ChannelInApp {
		t.Fatalf("empty channels = %v", got)
	}
}

func TestWeatherConfigChannels(t *testing.T) {
	cfg := WeatherConfig{Channels: `["email","bark","email"]`}
	if got := cfg.channels(); strings.Join(got, ",") != "inapp,email,bark" {
		t.Fatalf("channels = %v", got)
	}
	cfg.Channels = "not-json"
	if got := cfg.channels(); len(got) != 1 || got[0] != ChannelInApp {
		t.Fatalf("malformed channels = %v", got)
	}
}

func TestWeatherDue(t *testing.T) {
	loc := shanghai()
	at := func(hour, minute int) time.Time {
		return time.Date(2026, 11, 14, hour, minute, 0, 0, loc)
	}
	lastPush := func(when time.Time, status string) *time.Time {
		value := when.UTC()
		_ = status
		return &value
	}
	cases := []struct {
		name string
		cfg  WeatherConfig
		now  time.Time
		want bool
	}{
		{name: "before morning", cfg: WeatherConfig{MorningTime: "08:00"}, now: at(7, 59), want: false},
		{name: "at morning", cfg: WeatherConfig{MorningTime: "08:00"}, now: at(8, 0), want: true},
		{name: "inside catch-up window", cfg: WeatherConfig{MorningTime: "08:00"}, now: at(11, 59), want: true},
		{name: "after catch-up window", cfg: WeatherConfig{MorningTime: "08:00"}, now: at(12, 1), want: false},
		{name: "already pushed today", cfg: WeatherConfig{MorningTime: "08:00", LastPushDate: "2026-11-14"}, now: at(9, 0), want: false},
		{name: "failed ten minutes ago", cfg: WeatherConfig{MorningTime: "08:00", LastStatus: "error", LastPushAt: lastPush(at(8, 50), "error")}, now: at(9, 0), want: false},
		{name: "failed thirty-one minutes ago", cfg: WeatherConfig{MorningTime: "08:00", LastStatus: "error", LastPushAt: lastPush(at(8, 29), "error")}, now: at(9, 0), want: true},
		{name: "manual ok does not block", cfg: WeatherConfig{MorningTime: "08:00", LastStatus: "ok", LastPushAt: lastPush(at(8, 50), "ok")}, now: at(9, 0), want: true},
		{name: "empty morning defaults to eight", cfg: WeatherConfig{}, now: at(8, 0), want: true},
		{name: "invalid morning defaults to eight", cfg: WeatherConfig{MorningTime: "bad"}, now: at(8, 0), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := weatherDue(tc.cfg, tc.now); got != tc.want {
				t.Fatalf("weatherDue(%+v, %s) = %v, want %v", tc.cfg, tc.now, got, tc.want)
			}
		})
	}
}

func TestBuildWeatherMessage(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, loc)
	cfg := WeatherConfig{LocationName: "杭州"}

	var current qweatherCurrent
	current.Condition.Text = "小雨"
	current.Temperature.Value = 15.4
	current.Humidity = 0.68
	current.Wind.Scale = 3
	current.Wind.Direction.Compass = "se"

	daily := []qweatherDay{
		{ForecastStartTime: "2026-11-14T00:00:00+08:00", TemperatureMax: qweatherValue{Value: 20}, TemperatureMin: qweatherValue{Value: 12}, Daytime: qweatherDayPart{Condition: qweatherCondition{Text: "小雨"}, Precipitation: qweatherPrecipitation{Probability: 0.7, Type: "rain"}}},
		{ForecastStartTime: "2026-11-15T00:00:00+08:00", TemperatureMax: qweatherValue{Value: 16}, TemperatureMin: qweatherValue{Value: 10}, Daytime: qweatherDayPart{Condition: qweatherCondition{Text: "中雨"}, Precipitation: qweatherPrecipitation{Probability: 0.8, Type: "rain"}}},
		{ForecastStartTime: "2026-11-16T00:00:00+08:00", TemperatureMax: qweatherValue{Value: 18}, TemperatureMin: qweatherValue{Value: 9}, Daytime: qweatherDayPart{Condition: qweatherCondition{Text: "多云"}, Precipitation: qweatherPrecipitation{Probability: 0.1, Type: "none"}}},
		{ForecastStartTime: "2026-11-17T00:00:00+08:00", TemperatureMax: qweatherValue{Value: 19}, TemperatureMin: qweatherValue{Value: 8}, Daytime: qweatherDayPart{Condition: qweatherCondition{Text: "晴"}, Precipitation: qweatherPrecipitation{Probability: 0, Type: "none"}}},
	}
	alerts := []qweatherAlert{{
		MessageType: struct {
			Code string `json:"code"`
		}{Code: "alert"},
		EventType: struct {
			Name string `json:"name"`
			Code string `json:"code"`
		}{Name: "大风"},
		Color: struct {
			Code string `json:"code"`
		}{Code: "blue"},
		ExpireTime: "2026-11-14T20:00:00+08:00",
		Headline:   "预计有大风",
	}}

	msg := buildWeatherMessage(cfg, current, daily, alerts, now)
	if msg.Title != "天气提醒（有预警） · 杭州" {
		t.Fatalf("title = %q", msg.Title)
	}
	for _, want := range []string{
		"当前 15℃ 小雨，东南风3级，湿度 68%",
		"今日 小雨 12~20℃",
		"降水概率 70%",
		"11-15 周日 中雨 10~16℃ 降水 80% 带伞",
		"⚠️ 大风蓝色预警：预计有大风",
	} {
		if !strings.Contains(msg.Body, want) {
			t.Fatalf("body missing %q: %s", want, msg.Body)
		}
	}
}

func TestBuildWeatherMessageNightRain(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, loc)
	cfg := WeatherConfig{LocationName: "杭州"}
	current := qweatherCurrent{Condition: qweatherCondition{Text: "晴"}, Temperature: qweatherValue{Value: 20}}
	daily := []qweatherDay{
		{
			ForecastStartTime: "2026-11-14T00:00:00+08:00",
			TemperatureMax:    qweatherValue{Value: 22},
			TemperatureMin:    qweatherValue{Value: 14},
			Daytime:           qweatherDayPart{Condition: qweatherCondition{Text: "晴"}, Precipitation: qweatherPrecipitation{Probability: 0, Type: "none"}},
			Nighttime:         qweatherDayPart{Condition: qweatherCondition{Text: "小雨"}, Precipitation: qweatherPrecipitation{Probability: 0.8, Type: "rain"}},
		},
		{
			ForecastStartTime: "2026-11-15T00:00:00+08:00",
			TemperatureMax:    qweatherValue{Value: 18},
			TemperatureMin:    qweatherValue{Value: 12},
			Daytime:           qweatherDayPart{Condition: qweatherCondition{Text: "多云"}, Precipitation: qweatherPrecipitation{Probability: 0.2, Type: "none"}},
			Nighttime:         qweatherDayPart{Condition: qweatherCondition{Text: "中雨"}, Precipitation: qweatherPrecipitation{Probability: 0.7, Type: "rain"}},
		},
	}
	msg := buildWeatherMessage(cfg, current, daily, nil, now)
	if !strings.Contains(msg.Title, "（有雨）") {
		t.Fatalf("title = %q, want rain marker", msg.Title)
	}
	if !strings.Contains(msg.Body, "11-15 周日 多云 12~18℃ 降水 70% 带伞") {
		t.Fatalf("body = %q, want nighttime rain forecast", msg.Body)
	}
}

func TestBuildWeatherMessageCapsAlertsAtThree(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, loc)
	cfg := WeatherConfig{LocationName: "杭州"}
	alerts := make([]qweatherAlert, 0, 5)
	for i := 0; i < 5; i++ {
		alerts = append(alerts, qweatherAlert{
			MessageType: struct {
				Code string `json:"code"`
			}{Code: "alert"},
			EventType: struct {
				Name string `json:"name"`
				Code string `json:"code"`
			}{Name: "大风"},
			Color: struct {
				Code string `json:"code"`
			}{Code: "blue"},
			ExpireTime: "2026-11-14T20:00:00+08:00",
			Headline:   "预计有大风",
		})
	}
	msg := buildWeatherMessage(cfg, qweatherCurrent{Condition: qweatherCondition{Text: "晴"}, Temperature: qweatherValue{Value: 20}}, nil, alerts, now)
	if got := strings.Count(msg.Body, "⚠️"); got != 3 {
		t.Fatalf("alert lines = %d, want 3; body = %q", got, msg.Body)
	}
}

func TestBuildWeatherMessageRainTitleAndTruncation(t *testing.T) {
	loc := shanghai()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, loc)
	cfg := WeatherConfig{LocationName: strings.Repeat("杭", 100)}
	current := qweatherCurrent{Condition: qweatherCondition{Text: "小雨"}, Temperature: qweatherValue{Value: 15}}
	msg := buildWeatherMessage(cfg, current, nil, nil, now)
	if len([]rune(msg.Title)) > 80 {
		t.Fatalf("title not truncated: %d", len([]rune(msg.Title)))
	}
	if !strings.Contains(msg.Title, "（有雨）") {
		t.Fatalf("title = %q", msg.Title)
	}
	if !strings.Contains(msg.Body, "未来三天暂无预报数据。") {
		t.Fatalf("body = %q", msg.Body)
	}

	alerts := make([]qweatherAlert, 0, 12)
	for i := 0; i < 12; i++ {
		alerts = append(alerts, qweatherAlert{
			EventType: struct {
				Name string `json:"name"`
				Code string `json:"code"`
			}{Name: "暴雨"},
			Color: struct {
				Code string `json:"code"`
			}{Code: "red"},
			Headline: strings.Repeat("注意防范", 30),
		})
	}
	msg = buildWeatherMessage(cfg, current, nil, alerts, now)
	if len([]rune(msg.Body)) > 500 {
		t.Fatalf("body not truncated: %d", len([]rune(msg.Body)))
	}
}

func TestValidWeatherAlertsFiltersCancelledAndExpired(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, shanghai())
	alerts := []qweatherAlert{
		{MessageType: struct {
			Code string `json:"code"`
		}{Code: "cancel"}},
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
		{name: "empty name", in: WeatherConfigInput{Latitude: &lat, Longitude: &lon, MorningTime: "08:00"}},
		{name: "missing latitude", in: WeatherConfigInput{LocationName: "杭州", Longitude: &lon}},
		{name: "bad latitude", in: WeatherConfigInput{LocationName: "杭州", Latitude: &badLat, Longitude: &lon}},
		{name: "bad longitude", in: WeatherConfigInput{LocationName: "杭州", Latitude: &lat, Longitude: &badLon}},
		{name: "bad morning", in: WeatherConfigInput{LocationName: "杭州", Latitude: &lat, Longitude: &lon, MorningTime: "8点"}},
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
	morning := localNow.Add(-time.Minute)
	if morning.Format("2006-01-02") != localNow.Format("2006-01-02") {
		morning = localNow
	}
	cfg := WeatherConfig{
		UserID: 7, Enabled: true, LocationName: "杭州",
		Latitude: 30.27, Longitude: 120.15, MorningTime: morning.Format("15:04"),
		Channels: `["inapp"]`,
	}
	if err := appDB.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}

	msg, err := pushWeather(appDB, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Title, "杭州") {
		t.Fatalf("title = %q", msg.Title)
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
	if saved.LastPushDate != today || saved.LastStatus != "ok" || saved.LastPushAt == nil {
		t.Fatalf("saved = %+v", saved)
	}

	// A manual push must not consume the day's scheduled slot.
	before := saved.LastPushDate
	if _, err := pushWeather(appDB, saved, true); err != nil {
		t.Fatal(err)
	}
	var after WeatherConfig
	if err := appDB.First(&after, cfg.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.LastPushDate != before {
		t.Fatalf("manual push changed last_push_date: %q -> %q", before, after.LastPushDate)
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
	if dto.MorningTime != "08:00" || len(dto.Channels) != 1 || dto.Channels[0] != ChannelInApp {
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
		MorningTime: "08:00", Channels: []string{"inapp"}, Enabled: &disabled,
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
}
func TestBuildWeatherMessageAcceptsPercentUnits(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, shanghai())
	current := qweatherCurrent{
		Condition:   qweatherCondition{Text: "多云"},
		Temperature: qweatherValue{Value: 20},
		Humidity:    68,
	}
	day := qweatherDay{
		ForecastStartTime: "2026-11-14T00:00:00+08:00",
		TemperatureMin:    qweatherValue{Value: 12},
		TemperatureMax:    qweatherValue{Value: 20},
		Daytime: qweatherDayPart{
			Condition:     qweatherCondition{Text: "小雨"},
			Precipitation: qweatherPrecipitation{Probability: 70, Type: "rain"},
		},
	}
	msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, current, []qweatherDay{day}, nil, now)
	if !strings.Contains(msg.Body, "湿度 68%") {
		t.Fatalf("humidity should stay 68%%: %s", msg.Body)
	}
	if !strings.Contains(msg.Body, "降水概率 70%") {
		t.Fatalf("probability should stay 70%%: %s", msg.Body)
	}
}
func TestBuildWeatherMessageParsesMinutePrecisionTimestamps(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, shanghai())
	daily := []qweatherDay{
		{
			ForecastStartTime: "2026-11-14T00:00+08:00",
			TemperatureMin:    qweatherValue{Value: 12},
			TemperatureMax:    qweatherValue{Value: 20},
			Daytime:           qweatherDayPart{Condition: qweatherCondition{Text: "晴"}},
		},
		{
			ForecastStartTime: "2026-11-15T00:00+08:00",
			TemperatureMin:    qweatherValue{Value: 10},
			TemperatureMax:    qweatherValue{Value: 16},
			Daytime:           qweatherDayPart{Condition: qweatherCondition{Text: "小雨"}, Precipitation: qweatherPrecipitation{Probability: 0.8, Type: "rain"}},
		},
	}
	expired := qweatherAlert{ExpireTime: "2026-11-13T20:00+08:00", Headline: "已过期的大风"}
	expired.MessageType.Code = "alert"
	expired.EventType.Name = "大风"
	alerts := []qweatherAlert{expired}
	msg := buildWeatherMessage(WeatherConfig{LocationName: "杭州"}, qweatherCurrent{}, daily, alerts, now)
	if !strings.Contains(msg.Body, "11-15 周日 小雨 10~16℃ 降水 80% 带伞") {
		t.Fatalf("minute-precision forecast should be parsed: %s", msg.Body)
	}
	if strings.Contains(msg.Body, "已过期的大风") {
		t.Fatalf("expired alert should be filtered: %s", msg.Body)
	}
}
