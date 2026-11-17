package reminder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		Channels: []string{}, MorningTime: "08:00", EveningTime: "20:00",
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
	if strings.TrimSpace(dto.MorningTime) == "" {
		dto.MorningTime = "08:00"
	}
	dto.EveningTime = cfg.EveningTime
	if strings.TrimSpace(dto.EveningTime) == "" {
		dto.EveningTime = "20:00"
	}
	dto.Channels = cfg.channels()
	dto.LastPushDate = cfg.LastPushDate
	dto.LastSlot = cfg.LastSlot
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

	morning, err := normalizeWeatherClock(in.MorningTime, "08:00")
	if err != nil {
		return WeatherConfig{}, err
	}
	evening, err := normalizeWeatherClock(in.EveningTime, "20:00")
	if err != nil {
		return WeatherConfig{}, err
	}
	if morning == evening {
		return WeatherConfig{}, errors.New("早间和晚间推送时间不能相同")
	}
	if morning > evening {
		return WeatherConfig{}, errors.New("晚间推送时间应晚于早间推送时间")
	}

	channels := normalizeWeatherChannels(in.Channels)

	cfg, hasExisting := getWeatherConfig(db, userID)
	if !hasExisting {
		cfg = WeatherConfig{UserID: userID, Enabled: true}
	}
	// 渠道默认不再自动包含站内消息；启用天气推送时至少要有一个渠道，
	// 否则每天抓取预报却永远不会送达。关闭状态允许留空。
	enabled := cfg.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if enabled && len(channels) == 0 {
		return WeatherConfig{}, errors.New("请至少选择一个天气通知渠道")
	}

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

	encoded, err := json.Marshal(channels)
	if err != nil {
		return WeatherConfig{}, errors.New("通知渠道处理失败")
	}
	cfg.Enabled = enabled
	cfg.Channels = string(encoded)
	cfg.LocationName = name
	cfg.Latitude = lat
	cfg.Longitude = lon
	cfg.MorningTime = morning
	cfg.EveningTime = evening
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

// normalizeWeatherClock validates one HH:MM setting. An empty value takes the
// default so old rows and partial clients keep working.
func normalizeWeatherClock(raw, fallback string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.Parse("15:04", raw)
	if err != nil {
		return "", errors.New("推送时间格式应为 HH:MM")
	}
	return parsed.Format("15:04"), nil
}

func weatherClock(cfg WeatherConfig, slot weatherSlot) string {
	raw, fallback := cfg.MorningTime, "08:00"
	if slot == weatherSlotEvening {
		raw, fallback = cfg.EveningTime, "20:00"
	}
	value, err := normalizeWeatherClock(raw, fallback)
	if err != nil {
		return fallback
	}
	return value
}

// weatherScheduledAt returns today's scheduled time for a slot. The evening
// slot is scheduled today even though its message describes tomorrow.
func weatherScheduledAt(cfg WeatherConfig, slot weatherSlot, local time.Time) time.Time {
	parsed, _ := time.Parse("15:04", weatherClock(cfg, slot))
	return time.Date(local.Year(), local.Month(), local.Day(), parsed.Hour(), parsed.Minute(), 0, 0, shanghai())
}

type weatherMessage struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// buildWeatherMessage keeps the old call shape for tests and callers that only
// have a forecast. Production pushes add the optional current observation.
func buildWeatherMessage(cfg WeatherConfig, slot weatherSlot, daily []qweatherDay, alerts []qweatherAlert, now time.Time) weatherMessage {
	return buildWeatherMessageWithCurrent(cfg, slot, qweatherCurrent{}, daily, alerts, now)
}

// buildWeatherMessageWithCurrent renders one slot's summary. It is deliberately
// pure so the wording, emoji choice and alert window can be tested without a
// database or network. The morning slot covers today; the evening slot covers
// tomorrow. Each message keeps the forecast headline first, then adds the
// details people actually use (current conditions, humidity, wind, sunrise,
// sunset, visibility) and a compact multi-day outlook.
func buildWeatherMessageWithCurrent(cfg WeatherConfig, slot weatherSlot, current qweatherCurrent, daily []qweatherDay, alerts []qweatherAlert, now time.Time) weatherMessage {
	loc := shanghai()
	local := now.In(loc)
	if !slot.valid() {
		slot = weatherSlotMorning
	}

	targetDate := weatherTargetDate(slot, local)
	slotAlerts := weatherAlertsForSlot(alerts, slot, now, loc)
	day := findForecastDay(daily, targetDate, loc)

	// Put the emoji and the slot label first so a very long location name can
	// never hide which day the message is about.
	title := weatherEmoji(day, len(slotAlerts) > 0) + " " + slot.dayLabel() + "天气"
	if name := strings.TrimSpace(cfg.LocationName); name != "" {
		title += " · " + name
	}

	lines := make([]string, 0, 10)
	if day == nil {
		lines = append(lines, weatherDayLabel(slot)+"暂无预报数据。")
	} else {
		lines = append(lines, weatherHeading(slot, *day, targetDate, loc))
		lines = append(lines, weatherDetailLines(*day, current)...)
	}
	if len(slotAlerts) > 0 && len(lines) > 0 {
		lines = append(lines, "")
	}
	for i, alert := range slotAlerts {
		if i >= 2 {
			break
		}
		lines = append(lines, formatWeatherAlert(alert))
	}
	if outlook := formatWeatherOutlook(daily, targetDate, loc); outlook != "" {
		lines = append(lines, outlook)
	}

	// Keep the ellipsis inside the documented limits: Notification.Body is 500
	// characters and the title should stay under 80.
	return weatherMessage{Title: truncateRunes(title, 79), Body: truncateRunes(strings.Join(lines, "\n"), 499)}
}

func weatherDayLabel(slot weatherSlot) string {
	if slot == weatherSlotEvening {
		return "明天"
	}
	return "今天"
}

func weatherHeading(slot weatherSlot, day qweatherDay, targetDate string, loc *time.Location) string {
	heading := weatherDayLabel(slot)
	if date := formatWeatherDate(targetDate, loc); date != "" {
		heading += " · " + date
	}
	return heading + " · " + dayConditionText(day)
}

var weatherWeekdays = [...]string{"日", "一", "二", "三", "四", "五", "六"}

func formatWeatherDate(date string, loc *time.Location) string {
	parsed, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d月%d日 周%s", parsed.Month(), parsed.Day(), weatherWeekdays[parsed.Weekday()])
}

// weatherDetailLines renders the compact block of facts between the headline
// and the alerts. Missing provider fields are omitted instead of printing
// meaningless zeroes.
func weatherDetailLines(day qweatherDay, current qweatherCurrent) []string {
	lines := make([]string, 0, 5)

	temp := fmt.Sprintf("🌡 %.0f~%.0f℃", day.TemperatureMin.Value, day.TemperatureMax.Value)
	if current.hasData() {
		live := make([]string, 0, 3)
		if text := strings.TrimSpace(current.Condition.Text); text != "" {
			live = append(live, text)
		}
		if current.Temperature.Unit != "" || current.Temperature.Value != 0 {
			live = append(live, fmt.Sprintf("%.0f℃", current.Temperature.Value))
		}
		if current.FeelsLike.Value != 0 {
			live = append(live, fmt.Sprintf("体感 %.0f℃", current.FeelsLike.Value))
		}
		if len(live) > 0 {
			temp += " · 实况 " + strings.Join(live, " ")
		}
	}
	lines = append(lines, temp)

	humidity := day.Daytime.Humidity
	if humidity == 0 {
		humidity = day.Nighttime.Humidity
	}
	if humidity == 0 {
		humidity = current.Humidity
	}
	probability := dayProbability(day)
	amount, amountUnit := dayPrecipitationAmount(day)
	parts := make([]string, 0, 3)
	if humidity > 0 {
		parts = append(parts, fmt.Sprintf("湿度 %.0f%%", percentValue(humidity)))
	}
	if probability > 0 {
		parts = append(parts, fmt.Sprintf("降水概率 %.0f%%", percentValue(probability)))
	}
	if amount > 0 {
		parts = append(parts, fmt.Sprintf("降水量 %.1f%s", amount, amountUnit))
	}
	if len(parts) > 0 {
		lines = append(lines, "💧 "+strings.Join(parts, " · "))
	}

	wind := day.Daytime.Wind
	if wind.Scale == 0 && wind.Speed.Value == 0 {
		wind = day.Nighttime.Wind
	}
	if wind.Scale == 0 && wind.Speed.Value == 0 {
		wind = current.Wind
	}
	parts = parts[:0]
	if direction := windDirectionText(wind.Direction.Compass); direction != "" {
		if wind.Scale > 0 {
			parts = append(parts, fmt.Sprintf("%s %.0f级", direction, wind.Scale))
		} else {
			parts = append(parts, direction)
		}
	}
	if wind.Speed.Value > 0 {
		unit := strings.TrimSpace(wind.Speed.Unit)
		if unit == "" {
			unit = "km/h"
		}
		parts = append(parts, fmt.Sprintf("%.0f%s", wind.Speed.Value, unit))
	}
	if current.Pressure.Value > 0 {
		parts = append(parts, "气压 "+formatWeatherMeasure(current.Pressure, "hPa"))
	}
	if len(parts) > 0 {
		lines = append(lines, "🌬 "+strings.Join(parts, " · "))
	}

	parts = parts[:0]
	if sunrise := daySunrise(day); sunrise != "" {
		parts = append(parts, "日出 "+sunrise)
	}
	if sunset := daySunset(day); sunset != "" {
		parts = append(parts, "日落 "+sunset)
	}
	if len(parts) > 0 {
		lines = append(lines, "🌅 "+strings.Join(parts, " · "))
	}
	if current.Visibility.Value > 0 {
		lines = append(lines, "👁 能见度 "+formatWeatherVisibility(current.Visibility))
	}
	if advice := precipitationAdvice(day); advice != "" {
		lines = append(lines, "☂️ "+advice)
	}
	return lines
}

func (c qweatherCurrent) hasData() bool {
	return strings.TrimSpace(c.Condition.Text) != "" || strings.TrimSpace(c.Temperature.Unit) != "" ||
		c.Humidity > 0 || c.Wind.Scale > 0 || c.Wind.Speed.Value > 0
}

func dayPrecipitationAmount(day qweatherDay) (float64, string) {
	amount := day.Daytime.Precipitation.Amount.Value
	unit := strings.TrimSpace(day.Daytime.Precipitation.Amount.Unit)
	if night := day.Nighttime.Precipitation.Amount; night.Value > amount {
		amount = night.Value
		if strings.TrimSpace(night.Unit) != "" {
			unit = strings.TrimSpace(night.Unit)
		}
	}
	if unit == "" {
		unit = "mm"
	}
	return amount, unit
}

func daySunrise(day qweatherDay) string {
	return formatWeatherClock(day.Astro.Sunrise, day.Sunrise)
}

func daySunset(day qweatherDay) string {
	return formatWeatherClock(day.Astro.Sunset, day.Sunset)
}

func formatWeatherClock(values ...string) string {
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if parsed, ok := parseWeatherTime(raw); ok {
			return parsed.In(shanghai()).Format("15:04")
		}
		if parsed, err := time.Parse("15:04", raw); err == nil {
			return parsed.Format("15:04")
		}
	}
	return ""
}

func formatWeatherMeasure(value qweatherValue, fallbackUnit string) string {
	unit := strings.TrimSpace(value.Unit)
	if unit == "" {
		unit = fallbackUnit
	}
	return fmt.Sprintf("%.0f%s", value.Value, unit)
}

func formatWeatherVisibility(value qweatherValue) string {
	unit := strings.ToLower(strings.TrimSpace(value.Unit))
	if unit == "" {
		if value.Value >= 1000 {
			unit = "m"
		} else {
			unit = "km"
		}
	}
	if unit == "m" && value.Value >= 1000 {
		return fmt.Sprintf("%.0fkm", value.Value/1000)
	}
	if unit == "km" {
		return fmt.Sprintf("%.0fkm", value.Value)
	}
	return fmt.Sprintf("%.0f%s", value.Value, unit)
}

func windDirectionText(compass string) string {
	raw := strings.TrimSpace(compass)
	switch strings.ToUpper(raw) {
	case "N":
		return "北风"
	case "NNE":
		return "东北偏北风"
	case "NE":
		return "东北风"
	case "ENE":
		return "东北偏东风"
	case "E":
		return "东风"
	case "ESE":
		return "东南偏东风"
	case "SE":
		return "东南风"
	case "SSE":
		return "东南偏南风"
	case "S":
		return "南风"
	case "SSW":
		return "西南偏南风"
	case "SW":
		return "西南风"
	case "WSW":
		return "西南偏西风"
	case "W":
		return "西风"
	case "WNW":
		return "西北偏西风"
	case "NW":
		return "西北风"
	case "NNW":
		return "西北偏北风"
	}
	if strings.HasSuffix(raw, "风") {
		return raw
	}
	return ""
}

// formatWeatherOutlook lists the next days after the slot's target date. It is
// intentionally compact so the whole summary still fits in a notification.
func formatWeatherOutlook(daily []qweatherDay, targetDate string, loc *time.Location) string {
	parts := make([]string, 0, 3)
	for _, day := range daily {
		date := forecastDate(day.ForecastStartTime, loc)
		if date == "" || date <= targetDate {
			continue
		}
		label := date
		if parsed, err := time.ParseInLocation("2006-01-02", date, loc); err == nil {
			label = parsed.Format("01/02")
		}
		parts = append(parts, fmt.Sprintf("%s %s %.0f~%.0f℃", label, dayConditionText(day), day.TemperatureMin.Value, day.TemperatureMax.Value))
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "📅 未来：" + strings.Join(parts, "｜")
}

// weatherTargetDate is the date whose forecast a slot reports. Evening looks
// one day ahead so the user knows tomorrow before going to bed.
func weatherTargetDate(slot weatherSlot, local time.Time) string {
	if slot == weatherSlotEvening {
		return local.AddDate(0, 0, 1).Format("2006-01-02")
	}
	return local.Format("2006-01-02")
}

func findForecastDay(daily []qweatherDay, date string, loc *time.Location) *qweatherDay {
	for i := range daily {
		if forecastDate(daily[i].ForecastStartTime, loc) == date {
			return &daily[i]
		}
	}
	return nil
}

// dayConditionText joins the daytime and nighttime conditions only when they
// differ ("多云转晴"), which keeps the line short and still tells the whole day.
func dayConditionText(day qweatherDay) string {
	dayText := strings.TrimSpace(day.Daytime.Condition.Text)
	nightText := strings.TrimSpace(day.Nighttime.Condition.Text)
	switch {
	case dayText == "" && nightText == "":
		return "天气"
	case dayText == "":
		return nightText
	case nightText == "" || dayText == nightText:
		return dayText
	default:
		return dayText + "转" + nightText
	}
}

func precipitationAdvice(day qweatherDay) string {
	rain := precipitationRisk(day.Daytime, "rain") || precipitationRisk(day.Nighttime, "rain")
	snow := precipitationRisk(day.Daytime, "snow") || precipitationRisk(day.Nighttime, "snow")
	switch {
	case rain && snow:
		return "可能有雨雪，注意出行安全"
	case rain:
		return "记得带伞"
	case snow:
		return "注意防滑保暖"
	default:
		return ""
	}
}

// weatherEmoji picks the one emoji used in the title. Alerts win because they
// are the most actionable signal; then snow, rain, thunder and finally the
// condition text.
func weatherEmoji(day *qweatherDay, hasAlert bool) string {
	if hasAlert {
		return "⚠️"
	}
	if day == nil {
		return "🌤️"
	}
	condition := dayConditionText(*day)
	if strings.Contains(condition, "雷") {
		return "⛈️"
	}
	if precipitationRisk(day.Daytime, "snow") || precipitationRisk(day.Nighttime, "snow") {
		return "❄️"
	}
	if precipitationRisk(day.Daytime, "rain") || precipitationRisk(day.Nighttime, "rain") {
		return "🌧️"
	}
	switch {
	case strings.Contains(condition, "晴"):
		return "☀️"
	case strings.Contains(condition, "多云"):
		return "⛅"
	case strings.Contains(condition, "阴"):
		return "☁️"
	case strings.Contains(condition, "雾"), strings.Contains(condition, "霾"):
		return "🌫️"
	case strings.Contains(condition, "风"), strings.Contains(condition, "沙"):
		return "🌬️"
	default:
		return "🌤️"
	}
}

// weatherAlertsForSlot keeps only alerts that overlap the slot's target day.
// An alert that expires tonight must not appear in the evening push about
// tomorrow, and one that starts the day after tomorrow must not appear either.
func weatherAlertsForSlot(alerts []qweatherAlert, slot weatherSlot, now time.Time, loc *time.Location) []qweatherAlert {
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	if slot == weatherSlotEvening {
		start = start.AddDate(0, 0, 1)
	}
	end := start.AddDate(0, 0, 1).Add(-time.Nanosecond)

	result := make([]qweatherAlert, 0, len(alerts))
	for _, alert := range validWeatherAlerts(alerts, now) {
		if expire, ok := parseWeatherTime(alert.ExpireTime); ok && !expire.After(start) {
			continue
		}
		if onset, ok := alertOnset(alert); ok && onset.After(end) {
			continue
		}
		result = append(result, alert)
	}
	return result
}

func alertOnset(alert qweatherAlert) (time.Time, bool) {
	for _, raw := range []string{alert.OnsetTime, alert.EffectiveTime, alert.IssuedTime} {
		if parsed, ok := parseWeatherTime(raw); ok {
			return parsed, true
		}
	}
	return time.Time{}, false
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

const (
	weatherForecastDays  = 7
	weatherCatchUpWindow = 4 * time.Hour
	weatherRetryInterval = 30 * time.Minute
)

// weatherForecast loads the current observation, a multi-day forecast and the
// active alerts. Seven days are requested so the evening slot still has three
// future days after its "tomorrow" target, even when the provider's "today"
// has already rolled over. The current observation is optional: a provider
// hiccup must not block the forecast that the user actually asked for.
func weatherForecast(ctx context.Context, client *qweatherClient, cfg WeatherConfig) (qweatherCurrent, []qweatherDay, []qweatherAlert, error) {
	daily, err := client.Daily(ctx, cfg.Latitude, cfg.Longitude, weatherForecastDays)
	if err != nil {
		return qweatherCurrent{}, nil, nil, err
	}
	alerts, err := client.Alerts(ctx, cfg.Latitude, cfg.Longitude)
	if err != nil {
		return qweatherCurrent{}, nil, nil, err
	}
	current, err := client.Current(ctx, cfg.Latitude, cfg.Longitude)
	if err != nil {
		current = qweatherCurrent{}
	}
	return current, daily, alerts, nil
}

// pushWeather fetches the forecast and delivers one slot's summary. Manual
// runs do not consume the scheduled slot, so trying a preview never silences
// the morning or evening push.
// weatherPushLocks serializes pushes per user. The one-minute scheduler and a
// manual "立即推送" can otherwise overlap and write two identical notifications.
var weatherPushLocks sync.Map

func weatherPushLock(userID uint) *sync.Mutex {
	value, _ := weatherPushLocks.LoadOrStore(userID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func pushWeather(db *gorm.DB, cfg WeatherConfig, manual bool, slot weatherSlot) (weatherMessage, error) {
	lock := weatherPushLock(cfg.UserID)
	lock.Lock()
	defer lock.Unlock()

	if !slot.valid() {
		slot = weatherSlotMorning
	}
	if len(cfg.channels()) == 0 {
		return weatherMessage{}, errors.New("请至少选择一个天气通知渠道")
	}

	// Re-read and re-check under the lock: a second scheduler tick may have
	// passed the outer due check before the first push finished.
	if !manual {
		fresh, ok := getWeatherConfig(db, cfg.UserID)
		if !ok {
			return weatherMessage{}, nil
		}
		dueSlot, due := dueWeatherSlot(fresh, time.Now())
		if !due {
			return weatherMessage{}, nil
		}
		cfg, slot = fresh, dueSlot
	} else if fresh, ok := getWeatherConfig(db, cfg.UserID); ok {
		cfg = fresh
	}

	host, apiKey, err := qweatherCredentials(db)
	if err != nil {
		return weatherMessage{}, err
	}
	client := weatherClientFactory(host, apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	current, daily, alerts, err := weatherForecast(ctx, client, cfg)
	if err != nil {
		return weatherMessage{}, err
	}
	msg := buildWeatherMessageWithCurrent(cfg, slot, current, daily, alerts, time.Now())

	now := time.Now().UTC()
	localToday := time.Now().In(shanghai()).Format("2006-01-02")
	failures := make([]string, 0)
	succeeded := 0
	for _, channel := range cfg.channels() {
		// The slot is part of the idempotency key so the morning and evening
		// pushes on the same day are both delivered.
		key := fmt.Sprintf("weather:%d:%s:%s:%s", cfg.ID, localToday, slot, channel)
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
		updates["last_slot"] = string(slot)
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
		slot, due := dueWeatherSlot(cfg, now)
		if !due {
			continue
		}
		if _, err := pushWeather(db, cfg, false, slot); err != nil {
			logger.Warn("reminder: 天气推送失败 user=%d slot=%s: %v", cfg.UserID, slot, err)
		}
	}
}

// dueWeatherSlot returns the slot that is owed right now. A slot stays due for
// four hours after its scheduled time, so a short outage still gets a push;
// LastPushDate + LastSlot make restarts idempotent per slot.
func dueWeatherSlot(cfg WeatherConfig, now time.Time) (weatherSlot, bool) {
	local := now.In(shanghai())
	if weatherRetryBlocked(cfg, now) {
		return "", false
	}
	morningDue := weatherSlotDue(cfg, weatherSlotMorning, local)
	eveningDue := weatherSlotDue(cfg, weatherSlotEvening, local)
	switch {
	case morningDue && eveningDue:
		// Overlapping catch-up windows are unusual; prefer the later slot so
		// the user does not get stale morning content in the evening.
		if weatherScheduledAt(cfg, weatherSlotEvening, local).After(weatherScheduledAt(cfg, weatherSlotMorning, local)) {
			return weatherSlotEvening, true
		}
		return weatherSlotMorning, true
	case morningDue:
		return weatherSlotMorning, true
	case eveningDue:
		return weatherSlotEvening, true
	default:
		return "", false
	}
}

func weatherSlotDue(cfg WeatherConfig, slot weatherSlot, local time.Time) bool {
	if weatherSlotPushed(cfg, slot, local.Format("2006-01-02")) {
		return false
	}
	scheduled := weatherScheduledAt(cfg, slot, local)
	return !local.Before(scheduled) && !local.After(scheduled.Add(weatherCatchUpWindow))
}

// weatherSlotPushed treats an empty LastSlot as the morning slot: rows written
// before the evening push existed only ever recorded a morning delivery.
func weatherSlotPushed(cfg WeatherConfig, slot weatherSlot, today string) bool {
	if cfg.LastPushDate != today {
		return false
	}
	if cfg.LastSlot == "" {
		return slot == weatherSlotMorning
	}
	return cfg.LastSlot == string(slot)
}

func weatherRetryBlocked(cfg WeatherConfig, now time.Time) bool {
	if cfg.LastPushAt == nil {
		return false
	}
	if cfg.LastStatus == "" || cfg.LastStatus == "ok" || cfg.LastStatus == "partial" {
		return false
	}
	return now.Sub(cfg.LastPushAt.UTC()) < weatherRetryInterval
}
