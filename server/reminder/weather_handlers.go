package reminder

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"smallgo/server/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func handleGetWeather(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, ok := getWeatherConfig(db, currentUserID(c))
		response.Success(c, weatherConfigDTO(db, cfg, ok))
	}
}

func handleSaveWeather(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in WeatherConfigInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "请求参数无效")
			return
		}
		cfg, err := SaveWeatherConfig(db, currentUserID(c), in)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, weatherConfigDTO(db, cfg, true))
	}
}

func handleDeleteWeather(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := DeleteWeatherConfig(db, currentUserID(c)); err != nil {
			response.ErrorInternal(c, "关闭天气提醒失败")
			return
		}
		response.Success(c, gin.H{"deleted": true})
	}
}

// weatherSlotFromRequest reads the optional slot query parameter. An empty
// value keeps the morning behaviour that older clients expect.
func weatherSlotFromRequest(c *gin.Context) (weatherSlot, bool) {
	raw := strings.TrimSpace(c.Query("slot"))
	if raw == "" {
		return weatherSlotMorning, true
	}
	slot := weatherSlot(raw)
	return slot, slot.valid()
}

func handleRunWeather(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slot, ok := weatherSlotFromRequest(c)
		if !ok {
			response.ErrorBadRequest(c, "时段参数无效，应为 morning 或 evening")
			return
		}
		userID := currentUserID(c)
		cfg, ok := getWeatherConfig(db, userID)
		if !ok {
			response.ErrorBadRequest(c, "尚未配置天气提醒")
			return
		}
		msg, err := pushWeather(db, cfg, true, slot)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		cfg, ok = getWeatherConfig(db, userID)
		response.Success(c, gin.H{"slot": slot, "message": msg, "config": weatherConfigDTO(db, cfg, ok)})
	}
}

func handlePreviewWeather(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slot, ok := weatherSlotFromRequest(c)
		if !ok {
			response.ErrorBadRequest(c, "时段参数无效，应为 morning 或 evening")
			return
		}
		cfg, ok := getWeatherConfig(db, currentUserID(c))
		if !ok {
			response.ErrorBadRequest(c, "尚未配置天气提醒")
			return
		}
		host, apiKey, err := qweatherCredentials(db)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		client := weatherProviderFactory(host, apiKey)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		current, daily, alerts, err := weatherForecast(ctx, client, cfg)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, gin.H{"slot": slot, "message": buildWeatherMessageWithCurrent(cfg, slot, current, daily, alerts, time.Now())})
	}
}

// handleQueryWeather returns structured weather for voice assistants and other
// API clients. A city can be supplied directly; when it is omitted the caller's
// saved weather-reminder location is used.
func handleQueryWeather(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		city := strings.TrimSpace(c.Query("city"))
		if len([]rune(city)) > 60 {
			response.ErrorBadRequest(c, "城市名称过长")
			return
		}
		days, ok := weatherQueryDays(c.Query("days"))
		if !ok {
			response.ErrorBadRequest(c, "天数参数无效，应为 1 到 7")
			return
		}
		host, apiKey, err := qweatherCredentials(db)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		client := weatherProviderFactory(host, apiKey)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		cfg := WeatherConfig{}
		if city == "" {
			stored, ok := getWeatherConfig(db, currentUserID(c))
			if !ok {
				response.ErrorBadRequest(c, "尚未配置天气提醒，请先选择城市或直接指定城市")
				return
			}
			cfg = stored
		} else {
			locations, lookupErr := client.LookupCity(ctx, city)
			if lookupErr != nil {
				response.ErrorBadRequest(c, lookupErr.Error())
				return
			}
			if len(locations) == 0 {
				response.ErrorBadRequest(c, "没有找到城市，请换一个名称试试")
				return
			}
			loc := locations[0]
			latitude, latErr := strconv.ParseFloat(strings.TrimSpace(loc.Lat), 64)
			longitude, lonErr := strconv.ParseFloat(strings.TrimSpace(loc.Lon), 64)
			if latErr != nil || lonErr != nil {
				response.ErrorBadRequest(c, "城市坐标无效，请换一个城市")
				return
			}
			cfg = WeatherConfig{LocationName: loc.Name, Latitude: latitude, Longitude: longitude}
		}

		current, daily, alerts, err := queryWeather(ctx, client, cfg, days)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, buildWeatherQueryResult(cfg, current, daily, alerts, time.Now()))
	}
}

func weatherQueryDays(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 3, true
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > 7 {
		return 0, false
	}
	return days, true
}

func handleWeatherLocations(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := strings.TrimSpace(c.Query("q"))
		if query == "" {
			response.ErrorBadRequest(c, "请输入城市名称")
			return
		}
		if len([]rune(query)) > 60 {
			response.ErrorBadRequest(c, "城市名称过长")
			return
		}
		host, apiKey, err := qweatherCredentials(db)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		client := weatherProviderFactory(host, apiKey)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		locations, err := client.LookupCity(ctx, query)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, locations)
	}
}

func handleAdminGetWeather(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		host, _, err := qweatherCredentials(db)
		if err != nil {
			response.Success(c, gin.H{"configured": false, "host": ""})
			return
		}
		response.Success(c, gin.H{"configured": true, "host": host})
	}
}

func handleAdminSaveWeather(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in struct {
			Host   string `json:"host"`
			APIKey string `json:"api_key"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "请求参数无效")
			return
		}
		host, err := normalizeQWeatherHost(in.Host)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		apiKey := strings.TrimSpace(in.APIKey)
		if apiKey == "" {
			existing, _ := providerSettings(db, qweatherProvider)
			apiKey = strings.TrimSpace(existing["api_key"])
		}
		if apiKey == "" {
			// A deployment can supply the key through the environment; keep it
			// when the administrator only updates the host.
			apiKey = strings.TrimSpace(os.Getenv("QWEATHER_API_KEY"))
		}
		if apiKey == "" {
			response.ErrorBadRequest(c, "请填写 API Key")
			return
		}
		// Validate before persisting: a typo in host or key otherwise only
		// surfaces at the next morning's push.
		client := weatherProviderFactory(host, apiKey)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := client.Current(ctx, 39.92, 116.41); err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		if err := saveProviderSettings(db, qweatherProvider, map[string]string{"host": host, "api_key": apiKey}); err != nil {
			response.ErrorInternal(c, "保存和风天气配置失败")
			return
		}
		response.Success(c, gin.H{"configured": true, "host": host})
	}
}
