package reminder

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeQWeatherHost(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "https://abc.qweatherapi.com/", want: "https://abc.qweatherapi.com"},
		{in: "abc.qweatherapi.com", want: "https://abc.qweatherapi.com"},
		{in: " http://127.0.0.1:8080/ ", want: "http://127.0.0.1:8080"},
		{in: "http://localhost:9000", want: "http://localhost:9000"},
		{in: "http://example.com", wantErr: true},
		{in: "ftp://example.com", wantErr: true},
		{in: "https://user:pass@example.com", wantErr: true},
		{in: "https://example.com?x=1", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tc := range cases {
		got, err := normalizeQWeatherHost(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("normalizeQWeatherHost(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizeQWeatherHost(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeQWeatherHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQWeatherClientRequests(t *testing.T) {
	const key = "test-key"
	var seen []string
	client := newQWeatherClient("https://weather.test", key)
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-QW-Api-Key"); got != key {
			t.Errorf("X-QW-Api-Key = %q, want %q", got, key)
		}
		seen = append(seen, req.URL.Path)
		switch {
		case strings.HasPrefix(req.URL.Path, "/weather/v1/current/"):
			if req.URL.Query().Get("localTime") != "true" || req.URL.Query().Get("lang") != "zh" {
				t.Errorf("current query = %s", req.URL.RawQuery)
			}
			return caldavStubResponse(http.StatusOK, `{"validTime":"2026-11-14T08:00:00+08:00","condition":{"text":"小雨","code":"305"},"temperature":{"value":15.4,"unit":"°C"},"humidity":0.68,"wind":{"direction":{"degree":135,"compass":"se"},"speed":{"value":12},"scale":3},"precipitation":{"amount":{"value":0.4},"type":"rain"}}`, nil), nil
		case strings.HasPrefix(req.URL.Path, "/weather/v1/daily/"):
			if req.URL.Query().Get("days") != "4" {
				t.Errorf("daily days = %q", req.URL.Query().Get("days"))
			}
			return caldavStubResponse(http.StatusOK, `{"days":[{"forecastStartTime":"2026-11-14T00:00:00+08:00","temperatureMax":{"value":20},"temperatureMin":{"value":12},"daytime":{"condition":{"text":"小雨"},"precipitation":{"probability":0.7,"type":"rain"}}}]}`, nil), nil
		case strings.HasPrefix(req.URL.Path, "/weatheralert/v1/current/"):
			return caldavStubResponse(http.StatusOK, `{"metadata":{"zeroResult":false},"alerts":[{"id":"a1","messageType":{"code":"alert"},"eventType":{"name":"大风"},"color":{"code":"blue"},"expireTime":"2099-01-01T00:00:00+08:00","headline":"预计有大风"}]}`, nil), nil
		case req.URL.Path == "/geo/v2/city/lookup":
			if req.URL.Query().Get("location") != "杭州" || req.URL.Query().Get("range") != "cn" {
				t.Errorf("geo query = %s", req.URL.RawQuery)
			}
			return caldavStubResponse(http.StatusOK, `{"location":[{"name":"杭州","id":"101210101","lat":"30.27","lon":"120.15","adm1":"浙江省","adm2":"杭州","tz":"Asia/Shanghai"}]}`, nil), nil
		default:
			return caldavStubResponse(http.StatusNotFound, `{}`, nil), nil
		}
	})}
	ctx := context.Background()

	current, err := client.Current(ctx, 39.92, 116.41)
	if err != nil {
		t.Fatal(err)
	}
	if current.Condition.Text != "小雨" || current.Temperature.Value != 15.4 || current.Wind.Scale != 3 {
		t.Fatalf("unexpected current: %+v", current)
	}

	daily, err := client.Daily(ctx, 39.92, 116.41, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 1 || daily[0].Daytime.Precipitation.Probability != 0.7 {
		t.Fatalf("unexpected daily: %+v", daily)
	}

	alerts, err := client.Alerts(ctx, 39.92, 116.41)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].EventType.Name != "大风" {
		t.Fatalf("unexpected alerts: %+v", alerts)
	}

	locations, err := client.LookupCity(ctx, "杭州")
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 1 || locations[0].Lat != "30.27" {
		t.Fatalf("unexpected locations: %+v", locations)
	}

	if len(seen) != 4 {
		t.Fatalf("expected 4 requests, got %v", seen)
	}
}

func TestQWeatherClientZeroAlerts(t *testing.T) {
	client := newQWeatherClient("https://weather.test", "k")
	client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return caldavStubResponse(http.StatusOK, `{"metadata":{"zeroResult":true},"alerts":[]}`, nil), nil
	})}
	alerts, err := client.Alerts(context.Background(), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no alerts, got %+v", alerts)
	}
}

func TestQWeatherHTTPErrorMapping(t *testing.T) {
	cases := []struct {
		status  int
		body    string
		contain string
	}{
		{status: http.StatusUnauthorized, body: `{"error":{"detail":"bad key"}}`, contain: "API Key 无效"},
		{status: http.StatusForbidden, body: `{}`, contain: "无可用额度"},
		{status: http.StatusTooManyRequests, body: `{}`, contain: "过于频繁"},
		{status: http.StatusBadRequest, body: `{"error":{"detail":"latitude invalid"}}`, contain: "latitude invalid"},
		{status: http.StatusInternalServerError, body: `{}`, contain: "500"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			client := newQWeatherClient("https://weather.test", "k")
			client.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return caldavStubResponse(tc.status, tc.body, nil), nil
			})}
			_, err := client.Current(context.Background(), 1, 2)
			if err == nil || !strings.Contains(err.Error(), tc.contain) {
				t.Fatalf("error = %v, want containing %q", err, tc.contain)
			}
		})
	}
}
