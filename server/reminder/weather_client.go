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
)

// QWeather v1 response shapes. Only the fields the daily summary uses are
// decoded; unknown fields are ignored on purpose so a provider addition does
// not break delivery.
type qweatherValue struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

type qweatherCondition struct {
	Text string `json:"text"`
	Code string `json:"code"`
}

type qweatherWind struct {
	Direction struct {
		Degree  float64 `json:"degree"`
		Compass string  `json:"compass"`
	} `json:"direction"`
	Speed qweatherValue `json:"speed"`
	Scale float64       `json:"scale"`
}

type qweatherPrecipitation struct {
	Amount      qweatherValue `json:"amount"`
	Probability float64       `json:"probability"`
	Type        string        `json:"type"`
}

type qweatherCurrent struct {
	ValidTime     string                `json:"validTime"`
	Condition     qweatherCondition     `json:"condition"`
	Temperature   qweatherValue         `json:"temperature"`
	FeelsLike     qweatherValue         `json:"feelsLike"`
	Humidity      float64               `json:"humidity"`
	Wind          qweatherWind          `json:"wind"`
	Precipitation qweatherPrecipitation `json:"precipitation"`
}

type qweatherDayPart struct {
	Condition      qweatherCondition     `json:"condition"`
	TemperatureMax qweatherValue         `json:"temperatureMax"`
	TemperatureMin qweatherValue         `json:"temperatureMin"`
	Wind           qweatherWind          `json:"wind"`
	Precipitation  qweatherPrecipitation `json:"precipitation"`
	Humidity       float64               `json:"humidity"`
}

type qweatherDay struct {
	ForecastStartTime string          `json:"forecastStartTime"`
	TemperatureMax    qweatherValue   `json:"temperatureMax"`
	TemperatureMin    qweatherValue   `json:"temperatureMin"`
	Daytime           qweatherDayPart `json:"daytime"`
	Nighttime         qweatherDayPart `json:"nighttime"`
}

type qweatherAlert struct {
	ID          string `json:"id"`
	SenderName  string `json:"senderName"`
	IssuedTime  string `json:"issuedTime"`
	MessageType struct {
		Code string `json:"code"`
	} `json:"messageType"`
	EventType struct {
		Name string `json:"name"`
		Code string `json:"code"`
	} `json:"eventType"`
	Severity string `json:"severity"`
	Color    struct {
		Code string `json:"code"`
	} `json:"color"`
	EffectiveTime string `json:"effectiveTime"`
	OnsetTime     string `json:"onsetTime"`
	ExpireTime    string `json:"expireTime"`
	Headline      string `json:"headline"`
	Description   string `json:"description"`
	Instruction   string `json:"instruction"`
}

type qweatherAlertsResponse struct {
	Metadata struct {
		ZeroResult bool `json:"zeroResult"`
	} `json:"metadata"`
	Alerts []qweatherAlert `json:"alerts"`
}

type qweatherLocation struct {
	Name string `json:"name"`
	ID   string `json:"id"`
	Lat  string `json:"lat"`
	Lon  string `json:"lon"`
	Adm1 string `json:"adm1"`
	Adm2 string `json:"adm2"`
	Tz   string `json:"tz"`
}

type qweatherClient struct {
	host   string
	apiKey string
	http   *http.Client
}

func newQWeatherClient(host, apiKey string) *qweatherClient {
	return &qweatherClient{host: host, apiKey: apiKey, http: &http.Client{Timeout: 15 * time.Second}}
}

// weatherClientFactory is a test seam: service code goes through it so tests
// can supply a stub transport instead of opening a real socket.
var weatherClientFactory = newQWeatherClient

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
