package reminder

import (
	"context"
	"math"
	"strings"
	"time"
)

// WeatherQueryResult is the structured weather payload used by API clients and
// the Xiaozhi MCP adapter. The summary fields reuse the notification wording so
// voice answers stay consistent with the scheduled morning/evening push.
type WeatherQueryResult struct {
	Location WeatherQueryLocation `json:"location"`
	Current  WeatherQueryCurrent  `json:"current"`
	Days     []WeatherQueryDay    `json:"days"`
	Alerts   []WeatherQueryAlert  `json:"alerts"`
	Title    string               `json:"title"`
	Summary  string               `json:"summary"`
}

type WeatherQueryLocation struct {
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type WeatherQueryCurrent struct {
	Available         bool    `json:"available"`
	ObservedAt        string  `json:"observed_at,omitempty"`
	Condition         string  `json:"condition,omitempty"`
	Temperature       float64 `json:"temperature"`
	TemperatureUnit   string  `json:"temperature_unit,omitempty"`
	FeelsLike         float64 `json:"feels_like"`
	FeelsLikeUnit     string  `json:"feels_like_unit,omitempty"`
	Humidity          float64 `json:"humidity"`
	WindDirection     string  `json:"wind_direction,omitempty"`
	WindScale         float64 `json:"wind_scale"`
	WindSpeed         float64 `json:"wind_speed"`
	WindSpeedUnit     string  `json:"wind_speed_unit,omitempty"`
	Precipitation     float64 `json:"precipitation"`
	PrecipitationUnit string  `json:"precipitation_unit,omitempty"`
	Pressure          float64 `json:"pressure"`
	PressureUnit      string  `json:"pressure_unit,omitempty"`
	Visibility        string  `json:"visibility,omitempty"`
}

type WeatherQueryDay struct {
	Date                     string  `json:"date"`
	Weekday                  string  `json:"weekday,omitempty"`
	Label                    string  `json:"label,omitempty"`
	Condition                string  `json:"condition"`
	TextDay                  string  `json:"text_day,omitempty"`
	TextNight                string  `json:"text_night,omitempty"`
	TemperatureMin           float64 `json:"temperature_min"`
	TemperatureMax           float64 `json:"temperature_max"`
	Humidity                 float64 `json:"humidity"`
	PrecipitationProbability float64 `json:"precipitation_probability"`
	PrecipitationAmount      float64 `json:"precipitation_amount"`
	PrecipitationUnit        string  `json:"precipitation_unit,omitempty"`
	WindDirection            string  `json:"wind_direction,omitempty"`
	WindScale                float64 `json:"wind_scale"`
	WindSpeed                float64 `json:"wind_speed"`
	WindSpeedUnit            string  `json:"wind_speed_unit,omitempty"`
	Sunrise                  string  `json:"sunrise,omitempty"`
	Sunset                   string  `json:"sunset,omitempty"`
}

type WeatherQueryAlert struct {
	ID            string `json:"id,omitempty"`
	Headline      string `json:"headline,omitempty"`
	EventType     string `json:"event_type,omitempty"`
	Severity      string `json:"severity,omitempty"`
	Color         string `json:"color,omitempty"`
	Description   string `json:"description,omitempty"`
	Instruction   string `json:"instruction,omitempty"`
	EffectiveTime string `json:"effective_time,omitempty"`
	ExpireTime    string `json:"expire_time,omitempty"`
}

// queryWeather fetches exactly the requested number of forecast days. The
// current observation and alerts are best-effort: a secondary endpoint outage
// must not make the whole weather answer unavailable.
func queryWeather(ctx context.Context, client *qweatherClient, cfg WeatherConfig, days int) (qweatherCurrent, []qweatherDay, []qweatherAlert, error) {
	if days < 1 || days > 7 {
		days = 3
	}
	daily, err := client.Daily(ctx, cfg.Latitude, cfg.Longitude, days)
	if err != nil {
		return qweatherCurrent{}, nil, nil, err
	}
	if len(daily) > days {
		daily = daily[:days]
	}
	current, err := client.Current(ctx, cfg.Latitude, cfg.Longitude)
	if err != nil {
		current = qweatherCurrent{}
	}
	alerts, err := client.Alerts(ctx, cfg.Latitude, cfg.Longitude)
	if err != nil {
		alerts = nil
	}
	return current, daily, alerts, nil
}

func buildWeatherQueryResult(cfg WeatherConfig, current qweatherCurrent, daily []qweatherDay, alerts []qweatherAlert, now time.Time) WeatherQueryResult {
	msg := buildWeatherMessageWithCurrent(cfg, weatherSlotMorning, current, daily, alerts, now)
	result := WeatherQueryResult{
		Location: WeatherQueryLocation{
			Name:      strings.TrimSpace(cfg.LocationName),
			Latitude:  cfg.Latitude,
			Longitude: cfg.Longitude,
		},
		Current: weatherQueryCurrentDTO(current),
		Days:    weatherQueryDayDTOs(daily),
		Alerts:  weatherQueryAlertDTOs(alerts, now),
		Title:   msg.Title,
		Summary: msg.Body,
	}
	if result.Location.Name == "" {
		result.Location.Name = "当前位置"
	}
	return result
}

func weatherQueryCurrentDTO(current qweatherCurrent) WeatherQueryCurrent {
	dto := WeatherQueryCurrent{Available: current.hasData()}
	if !dto.Available {
		return dto
	}
	dto.ObservedAt = strings.TrimSpace(current.ValidTime)
	dto.Condition = strings.TrimSpace(current.Condition.Text)
	dto.Temperature = current.Temperature.Value
	dto.TemperatureUnit = weatherUnit(current.Temperature.Unit, "℃")
	dto.FeelsLike = current.FeelsLike.Value
	dto.FeelsLikeUnit = weatherUnit(current.FeelsLike.Unit, "℃")
	dto.Humidity = roundWeatherPercent(percentValue(current.Humidity))
	dto.WindDirection = windDirectionText(current.Wind.Direction.Compass)
	dto.WindScale = current.Wind.Scale
	dto.WindSpeed = current.Wind.Speed.Value
	dto.WindSpeedUnit = weatherUnit(current.Wind.Speed.Unit, "km/h")
	dto.Precipitation = current.Precipitation.Amount.Value
	dto.PrecipitationUnit = weatherUnit(current.Precipitation.Amount.Unit, "mm")
	dto.Pressure = current.Pressure.Value
	dto.PressureUnit = weatherUnit(current.Pressure.Unit, "hPa")
	if current.Visibility.Value > 0 {
		dto.Visibility = formatWeatherVisibility(current.Visibility)
	}
	return dto
}

func weatherQueryDayDTOs(daily []qweatherDay) []WeatherQueryDay {
	result := make([]WeatherQueryDay, 0, len(daily))
	for _, day := range daily {
		date := forecastDate(day.ForecastStartTime, shanghai())
		if date == "" {
			continue
		}
		wind := weatherWindForDay(day, qweatherCurrent{})
		amount, unit := dayPrecipitationAmount(day)
		item := WeatherQueryDay{
			Date:                     date,
			Condition:                dayConditionText(day),
			TextDay:                  strings.TrimSpace(day.Daytime.Condition.Text),
			TextNight:                strings.TrimSpace(day.Nighttime.Condition.Text),
			TemperatureMin:           day.TemperatureMin.Value,
			TemperatureMax:           day.TemperatureMax.Value,
			Humidity:                 roundWeatherPercent(percentValue(weatherHumidityForDay(day, qweatherCurrent{}))),
			PrecipitationProbability: roundWeatherPercent(percentValue(dayProbability(day))),
			PrecipitationAmount:      amount,
			PrecipitationUnit:        unit,
			WindDirection:            windDirectionText(wind.Direction.Compass),
			WindScale:                wind.Scale,
			WindSpeed:                wind.Speed.Value,
			WindSpeedUnit:            weatherUnit(wind.Speed.Unit, "km/h"),
			Sunrise:                  daySunrise(day),
			Sunset:                   daySunset(day),
		}
		if parsed, err := time.ParseInLocation("2006-01-02", date, shanghai()); err == nil {
			item.Weekday = "周" + weatherWeekdays[parsed.Weekday()]
			item.Label = formatWeatherDate(date, shanghai())
		}
		result = append(result, item)
	}
	return result
}

func weatherQueryAlertDTOs(alerts []qweatherAlert, now time.Time) []WeatherQueryAlert {
	active := validWeatherAlerts(alerts, now)
	result := make([]WeatherQueryAlert, 0, len(active))
	for _, alert := range active {
		if len(result) >= 5 {
			break
		}
		result = append(result, WeatherQueryAlert{
			ID:            strings.TrimSpace(alert.ID),
			Headline:      strings.TrimSpace(alert.Headline),
			EventType:     strings.TrimSpace(alert.EventType.Name),
			Severity:      strings.TrimSpace(alert.Severity),
			Color:         strings.TrimSpace(alert.Color.Code),
			Description:   strings.TrimSpace(alert.Description),
			Instruction:   strings.TrimSpace(alert.Instruction),
			EffectiveTime: strings.TrimSpace(alert.EffectiveTime),
			ExpireTime:    strings.TrimSpace(alert.ExpireTime),
		})
	}
	return result
}

// roundWeatherPercent removes binary floating point noise (for example
// 0.55*100 = 55.00000000000001) before the value reaches API and voice clients.
func roundWeatherPercent(value float64) float64 {
	return math.Round(value*10) / 10
}

func weatherUnit(unit, fallback string) string {
	if value := strings.TrimSpace(unit); value != "" {
		return value
	}
	return fallback
}
