package reminder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"smallgo/server/reminder/core"
)

// QWeather DTOs alias the provider-neutral core types so the client satisfies
// core.WeatherProvider without a second conversion layer.
type (
	qweatherValue         = core.WeatherValue
	qweatherCondition     = core.WeatherCondition
	qweatherWind          = core.WeatherWind
	qweatherPrecipitation = core.WeatherPrecipitation
	qweatherCurrent       = core.WeatherCurrent
	qweatherDayPart       = core.WeatherDayPart
	qweatherDay           = core.WeatherDay
	qweatherAlert         = core.WeatherAlert
	qweatherLocation      = core.WeatherLocation
)

type qweatherAlertsResponse struct {
	Metadata struct {
		ZeroResult bool `json:"zeroResult"`
	} `json:"metadata"`
	Alerts []qweatherAlert `json:"alerts"`
}

type qweatherClient struct {
	host   string
	apiKey string
	http   *http.Client
}

func newQWeatherClient(host, apiKey string) *qweatherClient {
	return &qweatherClient{host: host, apiKey: apiKey, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *qweatherClient) Name() string { return "qweather" }

// Forecast resolves a city then fetches its current observation, three-day
// outlook and active alerts. It backs the provider-neutral WeatherProvider
// contract; scheduled pushes call the individual endpoints directly.
func (c *qweatherClient) Forecast(ctx context.Context, city string) (core.WeatherForecast, error) {
	locations, err := c.LookupCity(ctx, city)
	if err != nil {
		return core.WeatherForecast{}, err
	}
	if len(locations) == 0 {
		return core.WeatherForecast{}, errors.New("未找到该城市")
	}
	loc := locations[0]
	lat, err := strconv.ParseFloat(loc.Lat, 64)
	if err != nil {
		return core.WeatherForecast{}, errors.New("城市坐标无效")
	}
	lon, err := strconv.ParseFloat(loc.Lon, 64)
	if err != nil {
		return core.WeatherForecast{}, errors.New("城市坐标无效")
	}
	current, err := c.Current(ctx, lat, lon)
	if err != nil {
		return core.WeatherForecast{}, err
	}
	daily, err := c.Daily(ctx, lat, lon, 3)
	if err != nil {
		return core.WeatherForecast{}, err
	}
	alerts, _ := c.Alerts(ctx, lat, lon)
	return core.WeatherForecast{City: loc, Current: current, Daily: daily, Alerts: alerts}, nil
}

// weatherProviderFactory resolves the configured weather backend through the
// registry. Tests replace this seam with a stub provider.
var weatherProviderFactory = func(host, apiKey string) core.WeatherProvider {
	if provider := weatherRegistry.Resolve("qweather", apiKey, host); provider != nil {
		return provider
	}
	return newQWeatherClient(host, apiKey)
}

// normalizeQWeatherHost accepts the console's dedicated API host with or
// without a scheme. Plain HTTP is only allowed for loopback so httptest can
// exercise the client without weakening production transport.
func normalizeQWeatherHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("请填写和风天气 API Host")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", errors.New("和风天气 API Host 格式不正确")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", errors.New("和风天气 API Host 必须使用 HTTPS")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("和风天气 API Host 必须使用 HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("和风天气 API Host 不能包含账号、查询参数或片段")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *qweatherClient) Current(ctx context.Context, lat, lon float64) (qweatherCurrent, error) {
	var out qweatherCurrent
	err := c.get(ctx, "/weather/v1/current/"+formatCoordinate(lat)+"/"+formatCoordinate(lon), url.Values{"localTime": {"true"}, "lang": {"zh"}}, &out)
	return out, err
}

func (c *qweatherClient) Daily(ctx context.Context, lat, lon float64, days int) ([]qweatherDay, error) {
	if days < 1 || days > 10 {
		days = 4
	}
	var out struct {
		Days []qweatherDay `json:"days"`
	}
	query := url.Values{"localTime": {"true"}, "lang": {"zh"}, "days": {strconv.Itoa(days)}}
	err := c.get(ctx, "/weather/v1/daily/"+formatCoordinate(lat)+"/"+formatCoordinate(lon), query, &out)
	return out.Days, err
}

func (c *qweatherClient) Alerts(ctx context.Context, lat, lon float64) ([]qweatherAlert, error) {
	var out qweatherAlertsResponse
	err := c.get(ctx, "/weatheralert/v1/current/"+formatCoordinate(lat)+"/"+formatCoordinate(lon), url.Values{"localTime": {"true"}, "lang": {"zh"}}, &out)
	if err != nil || out.Metadata.ZeroResult {
		return nil, err
	}
	return out.Alerts, nil
}

func (c *qweatherClient) LookupCity(ctx context.Context, query string) ([]qweatherLocation, error) {
	var out struct {
		Location []qweatherLocation `json:"location"`
	}
	values := url.Values{"location": {query}, "range": {"cn"}, "number": {"5"}, "lang": {"zh"}}
	err := c.get(ctx, "/geo/v2/city/lookup", values, &out)
	return out.Location, err
}

func formatCoordinate(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func (c *qweatherClient) get(ctx context.Context, path string, query url.Values, out interface{}) error {
	endpoint := strings.TrimRight(c.host, "/") + path
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("和风天气请求地址无效")
	}
	req.Header.Set("X-QW-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("和风天气服务暂时不可用，请稍后再试")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return qweatherHTTPError(resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("和风天气响应格式无效")
	}
	return nil
}

func qweatherHTTPError(status int, raw []byte) error {
	var problem struct {
		Error struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &problem)
	switch status {
	case http.StatusUnauthorized:
		return errors.New("和风天气 API Key 无效或已过期")
	case http.StatusForbidden:
		return errors.New("和风天气账号无可用额度或请求受限")
	case http.StatusTooManyRequests:
		return errors.New("和风天气请求过于频繁，请稍后再试")
	case http.StatusBadRequest:
		if detail := strings.TrimSpace(problem.Error.Detail); detail != "" {
			return fmt.Errorf("和风天气请求参数无效：%s", truncateRunes(detail, 120))
		}
		return errors.New("和风天气请求参数无效")
	default:
		return fmt.Errorf("和风天气服务返回 %d", status)
	}
}
