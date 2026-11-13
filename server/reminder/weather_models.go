package reminder

import (
	"encoding/json"
	"strings"
	"time"
)

// WeatherConfig is one user's weather-reminder subscription. The API
// credentials are administrator-scoped (ProviderConfig), so this row only
// stores where and when to push.
type WeatherConfig struct {
	ID     uint `gorm:"primarykey" json:"id"`
	UserID uint `gorm:"not null;uniqueIndex" json:"-"`

	Enabled      bool    `gorm:"not null;default:true" json:"enabled"`
	LocationName string  `gorm:"size:80;not null" json:"location_name"`
	Latitude     float64 `gorm:"not null" json:"latitude"`
	Longitude    float64 `gorm:"not null" json:"longitude"`
	// MorningTime is the local (Asia/Shanghai) HH:MM at which the daily
	// summary is sent.
	MorningTime string `gorm:"size:5;not null;default:08:00" json:"morning_time"`
	// Channels is a JSON array of channel keys. It is hidden from the API
	// because the DTO exposes the decoded list.
	Channels string `gorm:"type:text" json:"-"`

	// LastPushDate is the local date (YYYY-MM-DD) of the last scheduled push.
	// It makes the one-minute scheduler idempotent across restarts.
	LastPushDate string     `gorm:"size:10" json:"last_push_date"`
	LastPushAt   *time.Time `json:"last_push_at"`
	LastStatus   string     `gorm:"size:16" json:"last_status"`
	LastError    string     `gorm:"size:512" json:"last_error"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// channels returns the configured delivery channels, always including in-app
// notifications first. Unknown or empty values are dropped so a malformed row
// can never fan out to an unsupported channel.
func (c WeatherConfig) channels() []string {
	var raw []string
	if strings.TrimSpace(c.Channels) != "" {
		_ = json.Unmarshal([]byte(c.Channels), &raw)
	}
	return normalizeWeatherChannels(raw)
}

func normalizeWeatherChannels(raw []string) []string {
	seen := map[string]bool{ChannelInApp: true}
	result := []string{ChannelInApp}
	for _, channel := range raw {
		channel = strings.TrimSpace(channel)
		if channel == "" || !supportedChannels[channel] || seen[channel] {
			continue
		}
		seen[channel] = true
		result = append(result, channel)
	}
	return result
}

// WeatherConfigInput is the settings payload. Pointer fields distinguish
// "omitted" from "set to false/zero".
type WeatherConfigInput struct {
	LocationName string   `json:"location_name"`
	Latitude     *float64 `json:"latitude"`
	Longitude    *float64 `json:"longitude"`
	MorningTime  string   `json:"morning_time"`
	Channels     []string `json:"channels"`
	Enabled      *bool    `json:"enabled"`
}

// WeatherConfigDTO flattens the stored row and adds administrator state so the
// settings page can render the whole card from one response.
type WeatherConfigDTO struct {
	Configured      bool       `json:"configured"`
	AdminConfigured bool       `json:"admin_configured"`
	ID              uint       `json:"id"`
	Enabled         bool       `json:"enabled"`
	LocationName    string     `json:"location_name"`
	Latitude        float64    `json:"latitude"`
	Longitude       float64    `json:"longitude"`
	MorningTime     string     `json:"morning_time"`
	Channels        []string   `json:"channels"`
	LastPushDate    string     `json:"last_push_date"`
	LastPushAt      *time.Time `json:"last_push_at"`
	LastStatus      string     `json:"last_status"`
	LastError       string     `json:"last_error"`
}

const qweatherProvider = "qweather"
