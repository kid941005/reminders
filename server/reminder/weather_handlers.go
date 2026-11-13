package reminder

import (
	"context"
	"os"
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

func handleRunWeather(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := currentUserID(c)
		cfg, ok := getWeatherConfig(db, userID)
		if !ok {
			response.ErrorBadRequest(c, "尚未配置天气提醒")
			return
		}
		msg, err := pushWeather(db, cfg, true)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		cfg, ok = getWeatherConfig(db, userID)
		response.Success(c, gin.H{"message": msg, "config": weatherConfigDTO(db, cfg, ok)})
	}
}

func handlePreviewWeather(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
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
		client := weatherClientFactory(host, apiKey)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		current, err := client.Current(ctx, cfg.Latitude, cfg.Longitude)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		daily, err := client.Daily(ctx, cfg.Latitude, cfg.Longitude, 4)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		alerts, err := client.Alerts(ctx, cfg.Latitude, cfg.Longitude)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, buildWeatherMessage(cfg, current, daily, alerts, time.Now()))
	}
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
		client := weatherClientFactory(host, apiKey)
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
		client := weatherClientFactory(host, apiKey)
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
