package reminder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"smallgo/server/logger"

	"gorm.io/gorm"
)

// qweatherCredentials resolves the administrator-scoped API host and key.
// Database settings win; environment variables remain a deployment option.
func qweatherCredentials(db *gorm.DB) (string, string, error) {
	values, _ := providerSettings(db, qweatherProvider)
	host := strings.TrimSpace(values["host"])
	apiKey := strings.TrimSpace(values["api_key"])
	if host == "" {
		host = strings.TrimSpace(os.Getenv("QWEATHER_API_HOST"))
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("QWEATHER_API_KEY"))
	}
	if host == "" || apiKey == "" {
		return "", "", errors.New("管理员尚未配置和风天气")
	}
	normalized, err := normalizeQWeatherHost(host)
	if err != nil {
		return "", "", err
	}
	return normalized, apiKey, nil
}

func weatherAdminConfigured(db *gorm.DB) bool {
	_, _, err := qweatherCredentials(db)
	return err == nil
}

func getWeatherConfig(db *gorm.DB, userID uint) (WeatherConfig, bool) {
	var cfg WeatherConfig
	if err := db.Where("user_id = ?", userID).First(&cfg).Error; err != nil {
		return WeatherConfig{}, false
	}
	return cfg, true
}

func DeleteWeatherConfig(db *gorm.DB, userID uint) error {
	return db.Where("user_id = ?", userID).Delete(&WeatherConfig{}).Error
}

func weatherConfigDTO(db *gorm.DB, cfg WeatherConfig, ok bool) WeatherConfigDTO {
	dto := WeatherConfigDTO{
		Configured: ok, AdminConfigured: weatherAdminConfigured(db),
		Channels: []string{ChannelInApp}, MorningTime: "08:00",
	}
	if !ok {
		return dto
	}
	dto.ID = cfg.ID
	dto.Enabled = cfg.Enabled
	dto.LocationName = cfg.LocationName
	dto.Latitude = cfg.Latitude
	dto.Longitude = cfg.Longitude
	dto.MorningTime = cfg.MorningTime
	dto.Channels = cfg.channels()
	dto.LastPushDate = cfg.LastPushDate
	dto.LastPushAt = cfg.LastPushAt
	dto.LastStatus = cfg.LastStatus
	dto.LastError = cfg.LastError
	return dto
}

func SaveWeatherConfig(db *gorm.DB, userID uint, in WeatherConfigInput) (WeatherConfig, error) {
	name := strings.TrimSpace(in.LocationName)
	if name == "" {
		return WeatherConfig{}, errors.New("请填写地点名称")
	}
	if len([]rune(name)) > 80 {
		return WeatherConfig{}, errors.New("地点名称不能超过 80 个字符")
	}
	if in.Latitude == nil || in.Longitude == nil {
		return WeatherConfig{}, errors.New("请填写经纬度")
	}
	lat, lon := *in.Latitude, *in.Longitude
	if lat < -90 || lat > 90 {
		return WeatherConfig{}, errors.New("纬度应在 -90 到 90 之间")
	}
	if lon < -180 || lon > 180 {
		return WeatherConfig{}, errors.New("经度应在 -180 到 180 之间")
	}

	morning := strings.TrimSpace(in.MorningTime)
	if morning == "" {
		morning = "08:00"
	}
	parsed, err := time.Parse("15:04", morning)
	if err != nil {
		return WeatherConfig{}, errors.New("推送时间格式应为 HH:MM")
	}
	morning = parsed.Format("15:04")

	host, apiKey, err := qweatherCredentials(db)
	if err != nil {
		return WeatherConfig{}, err
	}
	client := weatherClientFactory(host, apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := client.Current(ctx, lat, lon); err != nil {
		return WeatherConfig{}, fmt.Errorf("地点校验失败：%w", err)
	}

	channels, err := json.Marshal(normalizeWeatherChannels(in.Channels))
	if err != nil {
		return WeatherConfig{}, errors.New("通知渠道处理失败")
	}

	cfg, hasExisting := getWeatherConfig(db, userID)
	if !hasExisting {
		cfg = WeatherConfig{UserID: userID, Enabled: true}
	}
	cfg.LocationName = name
	cfg.Latitude = lat
	cfg.Longitude = lon
	cfg.MorningTime = morning
	cfg.Channels = string(channels)
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	// GORM omits zero values for columns that declare a default, so a brand-new
	// row saved with Enabled=false would otherwise fall back to the column
	// default (true). Persist the explicit choice after the insert.
	wantEnabled := cfg.Enabled
	if err := db.Save(&cfg).Error; err != nil {
		return WeatherConfig{}, err
	}
	if !wantEnabled {
		if err := db.Model(&WeatherConfig{}).Where("id = ?", cfg.ID).Update("enabled", false).Error; err != nil {
			return WeatherConfig{}, err
		}
		cfg.Enabled = false
	}
	return cfg, nil
}

type weatherMessage struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// buildWeatherMessage renders the daily summary. It is deliberately pure so
// the wording and the precipitation/alert detection can be tested without a
// database or network.
func buildWeatherMessage(cfg WeatherConfig, current qweatherCurrent, daily []qweatherDay, alerts []qweatherAlert, now time.Time) weatherMessage {
	loc := shanghai()
	localNow := now.In(loc)
	today := localNow.Format("2006-01-02")

	validAlerts := validWeatherAlerts(alerts, now)
	rain := weatherHasRain(current, daily, today)
	snow := weatherHasSnow(current, daily, today)

	// Put the severity marker before the location so truncating a very long
	// location can never hide "有预警"/"有雨".
	title := "天气提醒"
	switch {
	case len(validAlerts) > 0:
		title += "（有预警）"
	case rain:
		title += "（有雨）"
	case snow:
		title += "（有雪）"
	}
	title += " · " + cfg.LocationName

	lines := make([]string, 0, 8)
	if currentLine := formatCurrentWeather(current); currentLine != "" {
		lines = append(lines, currentLine)
	}

	var todayDay *qweatherDay
	for i := range daily {
		if forecastDate(daily[i].ForecastStartTime, loc) == today {
			todayDay = &daily[i]
			break
		}
	}
	if todayDay == nil && len(daily) > 0 {
		todayDay = &daily[0]
	}
	if todayDay != nil {
		lines = append(lines, formatTodayWeather(*todayDay))
	}

	future := futureForecastDays(daily, today, loc, 3)
	for _, day := range future {
		lines = append(lines, formatFutureWeather(day, loc))
	}
	for i, alert := range validAlerts {
		if i >= 3 {
			break
		}
		lines = append(lines, formatWeatherAlert(alert))
	}
	if len(future) == 0 {
		lines = append(lines, "未来三天暂无预报数据。")
	} else if !rain && !snow && len(validAlerts) == 0 {
		lines = append(lines, "未来三天无明显降水。")
	}

	// Keep the ellipsis inside the documented limits: Notification.Body is 500
	// characters and the title should stay under 80.
	return weatherMessage{Title: truncateRunes(title, 79), Body: truncateRunes(strings.Join(lines, "\n"), 499)}
}

func formatCurrentWeather(current qweatherCurrent) string {
	if strings.TrimSpace(current.Condition.Text) == "" && current.Temperature.Value == 0 &&
		current.Humidity == 0 && current.Wind.Scale == 0 {
		return ""
	}
	text := fmt.Sprintf("当前 %.0f℃", current.Temperature.Value)
	if condition := strings.TrimSpace(current.Condition.Text); condition != "" {
		text += " " + condition
	}
	if scale := int(math.Round(current.Wind.Scale)); scale > 0 {
		direction := compassLabel(current.Wind.Direction.Compass)
		if direction != "" {
			text += "，" + direction
		}
		text += fmt.Sprintf("%d级", scale)
	}
	if current.Humidity > 0 {
		text += fmt.Sprintf("，湿度 %.0f%%", percentValue(current.Humidity))
	}
	return text
}

func formatTodayWeather(day qweatherDay) string {
	condition := dayPartCondition(day.Daytime, day.Nighttime)
	text := fmt.Sprintf("今日 %s %.0f~%.0f℃", condition, day.TemperatureMin.Value, day.TemperatureMax.Value)
	if probability := dayProbability(day); probability > 0 {
		text += fmt.Sprintf("，降水概率 %.0f%%", percentValue(probability))
	}
	return strings.TrimSpace(text)
}

func futureForecastDays(daily []qweatherDay, today string, loc *time.Location, limit int) []qweatherDay {
	result := make([]qweatherDay, 0, limit)
	for _, day := range daily {
		date := forecastDate(day.ForecastStartTime, loc)
		if date == "" || date <= today {
			continue
		}
		result = append(result, day)
		if len(result) >= limit {
			break
		}
	}
	return result
}

func dayPartCondition(daytime, nighttime qweatherDayPart) string {
	if text := strings.TrimSpace(daytime.Condition.Text); text != "" {
		return text
	}
	if text := strings.TrimSpace(nighttime.Condition.Text); text != "" {
		return text
	}
	return "天气"
}

func formatFutureWeather(day qweatherDay, loc *time.Location) string {
	condition := dayPartCondition(day.Daytime, day.Nighttime)
	date := forecastDate(day.ForecastStartTime, loc)
	label := date
	if parsed, err := time.ParseInLocation("2006-01-02", date, loc); err == nil {
		label = parsed.Format("01-02") + " " + weekdayLabel(parsed.Weekday())
	}
	text := fmt.Sprintf("%s %s %.0f~%.0f℃", label, condition, day.TemperatureMin.Value, day.TemperatureMax.Value)
	probability := dayProbability(day)
	if probability > 0 {
		text += fmt.Sprintf(" 降水 %.0f%%", percentValue(probability))
	}
	if precipitationNeedsUmbrella(day) {
		text += " 带伞"
	}
	return strings.TrimSpace(text)
}

func validWeatherAlerts(alerts []qweatherAlert, now time.Time) []qweatherAlert {
	result := make([]qweatherAlert, 0, len(alerts))
	for _, alert := range alerts {
		if strings.EqualFold(strings.TrimSpace(alert.MessageType.Code), "cancel") {
			continue
		}
		if expire := strings.TrimSpace(alert.ExpireTime); expire != "" {
			if parsed, ok := parseWeatherTime(expire); ok && !parsed.After(now) {
				continue
			}
		}
		result = append(result, alert)
	}
	return result
}

func formatWeatherAlert(alert qweatherAlert) string {
	name := strings.TrimSpace(alert.EventType.Name)
	if name == "" {
		name = "天气"
	}
	color := weatherColorLabel(alert.Color.Code)
	headline := strings.TrimSpace(alert.Headline)
	if headline == "" {
		headline = truncateRunes(strings.TrimSpace(alert.Description), 60)
	}
	if headline == "" {
		headline = "请注意防范"
	}
	return "⚠️ " + name + color + "预警：" + headline
}

func weatherColorLabel(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "blue":
		return "蓝色"
	case "yellow":
		return "黄色"
	case "amber":
		return "琥珀色"
	case "orange":
		return "橙色"
	case "red":
		return "红色"
	case "purple":
		return "紫色"
	case "black":
		return "黑色"
	case "white":
		return "白色"
	case "gray", "grey":
		return "灰色"
	case "green":
		return "绿色"
	default:
		return ""
	}
}

func compassLabel(compass string) string {
	switch strings.ToLower(strings.TrimSpace(compass)) {
	case "n":
		return "北风"
	case "nne":
		return "北东北风"
	case "ne":
		return "东北风"
	case "ene":
		return "东东北风"
	case "e":
		return "东风"
	case "ese":
		return "东东南风"
	case "se":
		return "东南风"
	case "sse":
		return "南东南风"
	case "s":
		return "南风"
	case "ssw":
		return "南西南风"
	case "sw":
		return "西南风"
	case "wsw":
		return "西西南风"
	case "w":
		return "西风"
	case "wnw":
		return "西西北风"
	case "nw":
		return "西北风"
	case "nnw":
		return "北西北风"
	default:
		return "风向不定"
	}
}

func weekdayLabel(day time.Weekday) string {
	return "周" + []string{"日", "一", "二", "三", "四", "五", "六"}[int(day)]
}

// parseWeatherTime accepts the ISO-8601 variants QWeather uses in practice:
// with seconds, without seconds, and with a space instead of "T".
func parseWeatherTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func forecastDate(raw string, loc *time.Location) string {
	parsed, ok := parseWeatherTime(raw)
	if !ok {
		return ""
	}
	return parsed.In(loc).Format("2006-01-02")
}

// fractionValue accepts both documented 0..1 ratios and percent values so a
// provider-side unit change cannot render "7000%".
func fractionValue(value float64) float64 {
	if value > 1 {
		return value / 100
	}
	return value
}

func percentValue(value float64) float64 {
	if value > 1 {
		return value
	}
	return value * 100
}

func dayProbability(day qweatherDay) float64 {
	daytime := fractionValue(day.Daytime.Precipitation.Probability)
	nighttime := fractionValue(day.Nighttime.Precipitation.Probability)
	if nighttime > daytime {
		return nighttime
	}
	return daytime
}

// precipitationNeedsUmbrella checks both halves of the day: a dry daytime
// forecast should not hide rain that is only expected at night.
func precipitationNeedsUmbrella(day qweatherDay) bool {
	return precipitationRisk(day.Daytime, "rain") || precipitationRisk(day.Daytime, "snow") ||
		precipitationRisk(day.Nighttime, "rain") || precipitationRisk(day.Nighttime, "snow")
}

func weatherHasRain(current qweatherCurrent, daily []qweatherDay, today string) bool {
	if strings.EqualFold(current.Precipitation.Type, "rain") || strings.Contains(current.Condition.Text, "雨") {
		return true
	}
	for _, day := range daily {
		date := forecastDate(day.ForecastStartTime, shanghai())
		if date == "" || date < today {
			continue
		}
		if precipitationRisk(day.Daytime, "rain") || precipitationRisk(day.Nighttime, "rain") {
			return true
		}
	}
	return false
}

func weatherHasSnow(current qweatherCurrent, daily []qweatherDay, today string) bool {
	if strings.EqualFold(current.Precipitation.Type, "snow") || strings.Contains(current.Condition.Text, "雪") {
		return true
	}
	for _, day := range daily {
		date := forecastDate(day.ForecastStartTime, shanghai())
		if date == "" || date < today {
			continue
		}
		if precipitationRisk(day.Daytime, "snow") || precipitationRisk(day.Nighttime, "snow") {
			return true
		}
	}
	return false
}

// precipitationRisk treats an explicit type or a high probability as a real
// chance of rain/snow, so a "多云 80%" day still earns an umbrella hint.
func precipitationRisk(part qweatherDayPart, kind string) bool {
	ptype := strings.ToLower(strings.TrimSpace(part.Precipitation.Type))
	if strings.EqualFold(ptype, kind) {
		return true
	}
	marker := "雨"
	if kind == "snow" {
		marker = "雪"
	}
	if strings.Contains(part.Condition.Text, marker) {
		return true
	}
	if fractionValue(part.Precipitation.Probability) < 0.5 {
		return false
	}
	if kind == "rain" {
		return ptype != "snow" && ptype != "ice"
	}
	return ptype == "snow" || ptype == "ice" || ptype == "mixed"
}

// pushWeather fetches current conditions, a four-day forecast and active
// alerts, then delivers one summary. manual runs do not consume the day's
// scheduled slot, so a preview does not silence the morning push.
// weatherPushLocks serializes pushes per user. The one-minute scheduler and a
// manual "立即推送" can otherwise overlap and write two identical notifications.
var weatherPushLocks sync.Map

func weatherPushLock(userID uint) *sync.Mutex {
	value, _ := weatherPushLocks.LoadOrStore(userID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func pushWeather(db *gorm.DB, cfg WeatherConfig, manual bool) (weatherMessage, error) {
	lock := weatherPushLock(cfg.UserID)
	lock.Lock()
	defer lock.Unlock()

	// Re-read and re-check under the lock: a second scheduler tick may have
	// passed the outer weatherDue check before the first push finished.
	if !manual {
		fresh, ok := getWeatherConfig(db, cfg.UserID)
		if !ok || !weatherDue(fresh, time.Now()) {
			return weatherMessage{}, nil
		}
		cfg = fresh
	}

	host, apiKey, err := qweatherCredentials(db)
	if err != nil {
		return weatherMessage{}, err
	}
	client := weatherClientFactory(host, apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	current, err := client.Current(ctx, cfg.Latitude, cfg.Longitude)
	if err != nil {
		return weatherMessage{}, err
	}
	daily, err := client.Daily(ctx, cfg.Latitude, cfg.Longitude, 4)
	if err != nil {
		return weatherMessage{}, err
	}
	alerts, err := client.Alerts(ctx, cfg.Latitude, cfg.Longitude)
	if err != nil {
		return weatherMessage{}, err
	}
	msg := buildWeatherMessage(cfg, current, daily, alerts, time.Now())

	now := time.Now().UTC()
	localToday := time.Now().In(shanghai()).Format("2006-01-02")
	failures := make([]string, 0)
	succeeded := 0
	for _, channel := range cfg.channels() {
		key := fmt.Sprintf("weather:%d:%s:%s", cfg.ID, localToday, channel)
		// One slow provider must not consume the budget of the remaining
		// channels, so each send gets its own deadline.
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, sendErr := sendTextChannel(sendCtx, db, channel, cfg.UserID, msg.Title, msg.Body, key, nil)
		sendCancel()
		if sendErr != nil {
			failures = append(failures, channelLabel(channel)+"："+safeError(sendErr))
			continue
		}
		succeeded++
	}

	status := "ok"
	if len(failures) > 0 {
		status = "partial"
		if succeeded == 0 {
			status = "error"
		}
	}
	updates := map[string]interface{}{
		"last_push_at": now,
		"last_status":  status,
		"last_error":   truncateRunes(strings.Join(failures, "；"), 511),
	}
	if !manual && succeeded > 0 {
		updates["last_push_date"] = localToday
	}
	_ = db.Model(&WeatherConfig{}).Where("id = ?", cfg.ID).Updates(updates).Error

	if succeeded == 0 {
		return msg, errors.New("所有通知渠道均发送失败：" + strings.Join(failures, "；"))
	}
	return msg, nil
}

// runDueWeatherPushes is the one-minute scheduler entry point.
func runDueWeatherPushes(db *gorm.DB) {
	var configs []WeatherConfig
	if err := db.Where("enabled = ?", true).Find(&configs).Error; err != nil {
		return
	}
	now := time.Now()
	for _, cfg := range configs {
		if !weatherDue(cfg, now) {
			continue
		}
		if _, err := pushWeather(db, cfg, false); err != nil {
			logger.Warn("reminder: 天气推送失败 user=%d: %v", cfg.UserID, err)
		}
	}
}

// weatherDue decides whether the daily summary is still owed today. The
// four-hour catch-up window avoids a push at midnight after a long outage,
// and LastPushDate keeps restarts from sending twice.
func weatherDue(cfg WeatherConfig, now time.Time) bool {
	local := now.In(shanghai())
	if cfg.LastPushDate == local.Format("2006-01-02") {
		return false
	}
	morning := strings.TrimSpace(cfg.MorningTime)
	if morning == "" {
		morning = "08:00"
	}
	parsed, err := time.Parse("15:04", morning)
	if err != nil {
		parsed, _ = time.Parse("15:04", "08:00")
	}
	scheduled := time.Date(local.Year(), local.Month(), local.Day(), parsed.Hour(), parsed.Minute(), 0, 0, shanghai())
	if local.Before(scheduled) || local.After(scheduled.Add(4*time.Hour)) {
		return false
	}
	// A failed attempt may be retried at most every 30 minutes; a successful
	// manual run should not block the scheduled one.
	if cfg.LastPushAt != nil && cfg.LastStatus != "ok" && cfg.LastStatus != "partial" {
		if now.Sub(cfg.LastPushAt.UTC()) < 30*time.Minute {
			return false
		}
	}
	return true
}
