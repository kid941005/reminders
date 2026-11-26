// Package core defines the stable contracts shared by the reminder domain and
// its pluggable integrations (notification channels, calendars, weather).
package core

import "context"

// WeatherValue is a numeric measurement paired with its provider unit.
type WeatherValue struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// WeatherCondition is a human readable sky condition.
type WeatherCondition struct {
	Text string `json:"text"`
	Code string `json:"code"`
}

// WeatherWind describes wind direction, speed and Beaufort scale.
type WeatherWind struct {
	Direction struct {
		Degree  float64 `json:"degree"`
		Compass string  `json:"compass"`
	} `json:"direction"`
	Speed WeatherValue `json:"speed"`
	Scale float64      `json:"scale"`
}

// WeatherPrecipitation describes precipitation amount and probability.
type WeatherPrecipitation struct {
	Amount      WeatherValue `json:"amount"`
	Probability float64      `json:"probability"`
	Type        string       `json:"type"`
}

// WeatherCurrent is the provider-neutral current observation.
type WeatherCurrent struct {
	ValidTime     string               `json:"validTime"`
	Condition     WeatherCondition     `json:"condition"`
	Temperature   WeatherValue         `json:"temperature"`
	FeelsLike     WeatherValue         `json:"feelsLike"`
	Humidity      float64              `json:"humidity"`
	Wind          WeatherWind          `json:"wind"`
	Precipitation WeatherPrecipitation `json:"precipitation"`
	Pressure      WeatherValue         `json:"pressure"`
	Visibility    WeatherValue         `json:"visibility"`
}

// WeatherDayPart is one half-day forecast segment.
type WeatherDayPart struct {
	Condition      WeatherCondition     `json:"condition"`
	TemperatureMax WeatherValue         `json:"temperatureMax"`
	TemperatureMin WeatherValue         `json:"temperatureMin"`
	Wind           WeatherWind          `json:"wind"`
	Precipitation  WeatherPrecipitation `json:"precipitation"`
	Humidity       float64              `json:"humidity"`
}

// WeatherDay is one daily forecast entry.
type WeatherDay struct {
	ForecastStartTime string         `json:"forecastStartTime"`
	TemperatureMax    WeatherValue   `json:"temperatureMax"`
	TemperatureMin    WeatherValue   `json:"temperatureMin"`
	Daytime           WeatherDayPart `json:"daytime"`
	Nighttime         WeatherDayPart `json:"nighttime"`
	Sunrise           string         `json:"sunrise"`
	Sunset            string         `json:"sunset"`
	Astro             struct {
		Sunrise string `json:"sunrise"`
		Sunset  string `json:"sunset"`
	} `json:"astro"`
}

// WeatherAlert is a provider-neutral severe weather alert.
type WeatherAlert struct {
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

// WeatherLocation is a resolved city with coordinates.
type WeatherLocation struct {
	Name string `json:"name"`
	ID   string `json:"id"`
	Lat  string `json:"lat"`
	Lon  string `json:"lon"`
	Adm1 string `json:"adm1"`
	Adm2 string `json:"adm2"`
	Tz   string `json:"tz"`
}

// WeatherForecast bundles a resolved location with its forecast.
type WeatherForecast struct {
	City    WeatherLocation
	Current WeatherCurrent
	Daily   []WeatherDay
	Alerts  []WeatherAlert
}

// WeatherProvider is an external weather backend. The domain depends only on
// this interface; concrete clients (for example QWeather) register an adapter at
// startup so adding another provider means registering a factory, not editing
// business code.
type WeatherProvider interface {
	Name() string
	Current(ctx context.Context, lat, lon float64) (WeatherCurrent, error)
	Daily(ctx context.Context, lat, lon float64, days int) ([]WeatherDay, error)
	Alerts(ctx context.Context, lat, lon float64) ([]WeatherAlert, error)
	LookupCity(ctx context.Context, query string) ([]WeatherLocation, error)
	Forecast(ctx context.Context, city string) (WeatherForecast, error)
}
