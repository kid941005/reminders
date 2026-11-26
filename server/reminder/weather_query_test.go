package reminder

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"smallgo/server/reminder/core"
	"time"

	"github.com/gin-gonic/gin"
)

func TestWeatherQueryDays(t *testing.T) {
	cases := []struct {
		raw  string
		want int
		ok   bool
	}{
		{raw: "", want: 3, ok: true},
		{raw: "1", want: 1, ok: true},
		{raw: "7", want: 7, ok: true},
		{raw: "0", ok: false},
		{raw: "8", ok: false},
		{raw: "abc", ok: false},
	}
	for _, tc := range cases {
		got, ok := weatherQueryDays(tc.raw)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("weatherQueryDays(%q) = %d,%v want %d,%v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBuildWeatherQueryResultNormalizesValues(t *testing.T) {
	loc := shanghai()
	today := time.Now().In(loc)
	day := weatherTestDay(today.Format("2006-01-02"), "小雨", 10, 20)
	day.Daytime.Humidity = 0.55
	day.Daytime.Precipitation.Probability = 0.7
	day.Daytime.Wind.Direction.Compass = "SE"
	day.Daytime.Wind.Scale = 3
	day.Daytime.Wind.Speed.Value = 12
	day.Astro.Sunrise = "06:30"
	day.Astro.Sunset = "17:10"

	current := qweatherCurrent{
		Condition:   qweatherCondition{Text: "小雨"},
		Temperature: qweatherValue{Value: 15.4, Unit: "℃"},
		Humidity:    0.68,
	}
	current.Wind.Direction.Compass = "SE"
	current.Wind.Scale = 3
	current.Wind.Speed = qweatherValue{Value: 8, Unit: "km/h"}
	active := weatherTestAlert("大风", "blue", today.Add(2*time.Hour).Format(time.RFC3339), "", "预计有大风")
	expired := weatherTestAlert("暴雨", "yellow", today.Add(-2*time.Hour).Format(time.RFC3339), "", "已过期")
	cancelled := weatherTestAlert("高温", "red", today.Add(2*time.Hour).Format(time.RFC3339), "", "已取消")
	cancelled.MessageType.Code = "cancel"

	result := buildWeatherQueryResult(WeatherConfig{LocationName: "杭州", Latitude: 30.25, Longitude: 120.17}, current, []qweatherDay{day}, []qweatherAlert{active, expired, cancelled}, today)
	if result.Location.Name != "杭州" {
		t.Fatalf("location = %q", result.Location.Name)
	}
	if !result.Current.Available || result.Current.Humidity != 68 {
		t.Fatalf("current = %+v", result.Current)
	}
	if len(result.Days) != 1 {
		t.Fatalf("days = %d", len(result.Days))
	}
	if result.Days[0].Humidity != 55 || result.Days[0].PrecipitationProbability != 70 {
		t.Fatalf("day values = %+v", result.Days[0])
	}
	if result.Days[0].WindDirection != "东南风" || result.Days[0].Sunrise != "06:30" {
		t.Fatalf("day wind/sun = %+v", result.Days[0])
	}
	if len(result.Alerts) != 1 || result.Alerts[0].EventType != "大风" {
		t.Fatalf("alerts = %+v", result.Alerts)
	}
	if result.Title == "" || result.Summary == "" {
		t.Fatalf("summary missing: %+v", result)
	}
}

func TestHandleQueryWeatherUsesSavedLocation(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()
	t.Setenv("QWEATHER_API_HOST", "https://weather.test")
	t.Setenv("QWEATHER_API_KEY", "test-key")

	const userID = 7
	if err := appDB.Create(&WeatherConfig{
		UserID: userID, Enabled: true, LocationName: "杭州",
		Latitude: 30.25, Longitude: 120.17,
		MorningTime: "08:00", EveningTime: "20:00", Channels: `["inapp"]`,
	}).Error; err != nil {
		t.Fatal(err)
	}

	oldFactory := weatherProviderFactory
	weatherProviderFactory = func(host, apiKey string) core.WeatherProvider { return weatherTestClient(t) }
	defer func() { weatherProviderFactory = oldFactory }()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/reminder/weather/query?days=2", nil)
	context.Set("userID", uint(userID))
	handleQueryWeather(appDB)(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Code int                `json:"code"`
		Data WeatherQueryResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != 0 || envelope.Data.Location.Name != "杭州" {
		t.Fatalf("unexpected response: %+v", envelope)
	}
	if len(envelope.Data.Days) != 2 {
		t.Fatalf("days = %d, want 2", len(envelope.Data.Days))
	}
	if envelope.Data.Current.Humidity != 68 {
		t.Fatalf("humidity = %v, want 68", envelope.Data.Current.Humidity)
	}
}

func TestHandleQueryWeatherResolvesCity(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()
	t.Setenv("QWEATHER_API_HOST", "https://weather.test")
	t.Setenv("QWEATHER_API_KEY", "test-key")

	oldFactory := weatherProviderFactory
	weatherProviderFactory = func(host, apiKey string) core.WeatherProvider { return weatherTestClient(t) }
	defer func() { weatherProviderFactory = oldFactory }()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/reminder/weather/query?city=杭州&days=1", nil)
	context.Set("userID", uint(7))
	handleQueryWeather(appDB)(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data WeatherQueryResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Location.Name != "杭州" || len(envelope.Data.Days) != 1 {
		t.Fatalf("unexpected response: %+v", envelope.Data)
	}
}

func TestHandleQueryWeatherRequiresLocation(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()
	t.Setenv("QWEATHER_API_HOST", "https://weather.test")
	t.Setenv("QWEATHER_API_KEY", "test-key")

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/reminder/weather/query", nil)
	context.Set("userID", uint(7))
	handleQueryWeather(appDB)(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
}
