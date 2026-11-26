package reminder

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"smallgo/server/response"
	"smallgo/server/sysconfig"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type deliveryError struct {
	Code      string
	Message   string
	Permanent bool
}

func (e *deliveryError) Error() string { return e.Message }

type sendResult struct {
	ExternalID string
}

var cnMobile = regexp.MustCompile(`^1[3-9]\d{9}$`)

func handleChannelStatuses(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		response.Success(c, channelStatuses(db, currentUserID(c)))
	}
}

func handleProviderStatuses(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		response.Success(c, gin.H{
			"email": gin.H{"configured": emailConfigured(db)}, "sms": gin.H{"configured": smsConfigured(db)},
			"feishu": gin.H{"configured": feishuConfigured(db)}, "qq": gin.H{"configured": qqConfigured(db)},
		})
	}
}

// The notification brand is the human-readable source shown in robot
// messages. It is separate from the sender mailbox so a friendly product name
// can be used consistently in Feishu and QQ.
func handleNotificationBrand(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet {
			response.Success(c, gin.H{"name": notificationBrand(db)})
			return
		}
		var in struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "通知来源名称无效")
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" || len([]rune(in.Name)) > 40 {
			response.ErrorBadRequest(c, "通知来源名称应为 1 到 40 个字符")
			return
		}
		if err := saveProviderSettings(db, "notification_brand", map[string]string{"name": in.Name}); err != nil {
			response.ErrorInternal(c, "保存通知来源名称失败")
			return
		}
		response.Success(c, gin.H{"name": in.Name})
	}
}

func handleSaveProvider(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		provider := strings.ToLower(c.Param("provider"))
		if provider != ChannelEmail && provider != ChannelSMS && provider != ChannelQQ {
			response.ErrorBadRequest(c, "不支持的通知服务")
			return
		}
		var values map[string]string
		if err := c.ShouldBindJSON(&values); err != nil {
			response.ErrorBadRequest(c, "配置参数无效")
			return
		}
		for k, v := range values {
			values[k] = strings.TrimSpace(v)
		}
		valid := false
		switch provider {
		case ChannelEmail:
			host := strings.TrimSpace(strings.ToLower(values["host"]))
			valid = strings.Contains(host, ".") && !strings.ContainsAny(host, " /:@") && values["from_address"] != ""
			if !valid {
				response.ErrorBadRequest(c, "SMTP 服务器应填写完整域名，例如 smtp.qq.com，而不是 SMTP")
				return
			}
		case ChannelSMS:
			webhook, err := url.ParseRequestURI(values["webhook_url"])
			valid = err == nil && (webhook.Scheme == "https" || webhook.Scheme == "http") && webhook.Host != ""
			if !valid {
				response.ErrorBadRequest(c, "短信 Webhook 应填写有效的 http 或 https 地址")
				return
			}
		case ChannelQQ:
			valid = values["app_id"] != "" && values["app_secret"] != ""
			if link := values["bot_link"]; link != "" {
				parsed, err := url.ParseRequestURI(link)
				if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
					response.ErrorBadRequest(c, "机器人主页应填写有效的 http 或 https 链接")
					return
				}
			}
		}
		if !valid {
			response.ErrorBadRequest(c, "请填写必填配置")
			return
		}
		if err := saveProviderSettings(db, provider, values); err != nil {
			response.ErrorInternal(c, "保存通知服务配置失败")
			return
		}
		response.Success(c, gin.H{"configured": true})
	}
}

func handleSaveFeishuProvider(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in struct {
			AppID     string `json:"app_id"`
			AppSecret string `json:"app_secret"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "配置参数无效")
			return
		}
		in.AppID, in.AppSecret = strings.TrimSpace(in.AppID), strings.TrimSpace(in.AppSecret)
		if in.AppID == "" || in.AppSecret == "" {
			response.ErrorBadRequest(c, "请填写 App ID 和 App Secret")
			return
		}
		secret, err := encryptTarget(db, in.AppID+"\n"+in.AppSecret)
		if err != nil {
			response.ErrorInternal(c, "加密飞书配置失败")
			return
		}
		row := ProviderConfig{Provider: ChannelFeishu, Secret: secret}
		if err := db.Where("provider = ?", ChannelFeishu).Assign(map[string]interface{}{"secret": secret}).FirstOrCreate(&row).Error; err != nil {
			response.ErrorInternal(c, "保存飞书配置失败")
			return
		}
		response.Success(c, gin.H{"configured": true})
	}
}

func handleBindChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channel := strings.ToLower(c.Param("channel"))
		if channel == ChannelInApp || !supportedChannels[channel] {
			response.ErrorBadRequest(c, "该渠道不支持绑定")
			return
		}
		var in struct {
			Target string `json:"target"`
			// Secret 目前只被钉钉使用：加签模式下机器人要求每次请求附带签名，
			// 密钥与 webhook 一起序列化进同一个加密字段，不单独建表。
			Secret string `json:"secret"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "绑定参数无效")
			return
		}
		target, err := normalizeTarget(channel, in.Target)
		if err == nil && channel == ChannelFeishu {
			target, err = resolveFeishuOpenID(c.Request.Context(), db, target)
		}
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		if channel == ChannelDingTalk {
			if secret := strings.TrimSpace(in.Secret); secret != "" {
				if !strings.HasPrefix(secret, "SEC") || len(secret) > 128 {
					response.ErrorBadRequest(c, "钉钉加签密钥格式无效（应为 SEC 开头的一串字符）")
					return
				}
				payload, marshalErr := json.Marshal(dingTalkTarget{Webhook: target, Secret: secret})
				if marshalErr != nil {
					response.ErrorInternal(c, "保存绑定失败")
					return
				}
				target = string(payload)
			}
		}
		// Email allows several receiving mailboxes per user; every other
		// channel keeps the single-binding replace semantics below.
		if channel == ChannelEmail {
			duplicate, err := emailTargetExists(db, currentUserID(c), target)
			if err != nil {
				response.ErrorInternal(c, "读取已有绑定失败")
				return
			}
			if duplicate {
				response.ErrorBadRequest(c, "该邮箱已绑定过")
				return
			}
		}
		encrypted, err := encryptTarget(db, target)
		if err != nil {
			response.ErrorInternal(c, "加密绑定信息失败")
			return
		}
		now := time.Now()
		binding := ChannelBinding{
			UserID: currentUserID(c), Channel: channel, Target: encrypted,
			TargetMasked: maskTarget(channel, target), Status: "active", VerifiedAt: &now,
		}
		var saveErr error
		if channel == ChannelEmail {
			saveErr = db.Create(&binding).Error
		} else {
			saveErr = db.Where("user_id = ? AND channel = ?", binding.UserID, channel).
				Assign(map[string]interface{}{
					"target": encrypted, "target_masked": binding.TargetMasked,
					"status": "active", "verified_at": &now, "last_error_code": "",
				}).FirstOrCreate(&binding).Error
		}
		if saveErr != nil {
			response.ErrorInternal(c, "保存绑定失败")
			return
		}
		publishDataChanged(binding.UserID, "channel", "bound", binding.ID, originClientID(c))
		response.Success(c, binding)
	}
}

// emailTargetExists compares the plaintext of existing email bindings, since
// the encrypted target is randomised and cannot be de-duplicated in SQL.
func emailTargetExists(db *gorm.DB, userID uint, target string) (bool, error) {
	var bindings []ChannelBinding
	if err := db.Where("user_id = ? AND channel = ?", userID, ChannelEmail).Find(&bindings).Error; err != nil {
		return false, err
	}
	for _, binding := range bindings {
		plain, err := decryptTarget(db, binding.Target)
		if err != nil {
			continue
		}
		if strings.EqualFold(plain, target) {
			return true, nil
		}
	}
	return false, nil
}

func handleDeleteChannelBinding(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channel := strings.ToLower(c.Param("channel"))
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil || channel == ChannelInApp || !supportedChannels[channel] {
			response.ErrorBadRequest(c, "绑定记录无效")
			return
		}
		result := db.Where("id = ? AND user_id = ? AND channel = ?", id, currentUserID(c), channel).Delete(&ChannelBinding{})
		if result.Error != nil {
			response.ErrorInternal(c, "删除绑定失败")
			return
		}
		if result.RowsAffected == 0 {
			response.ErrorBadRequest(c, "绑定记录不存在")
			return
		}
		publishDataChanged(currentUserID(c), "channel", "binding-deleted", uint(id), originClientID(c))
		response.Success(c, gin.H{"deleted": true})
	}
}

func handleToggleChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channel := strings.ToLower(c.Param("channel"))
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "渠道参数无效")
			return
		}
		status := "disabled"
		if in.Enabled {
			status = "active"
		}
		result := db.Model(&ChannelBinding{}).Where("user_id = ? AND channel = ?", currentUserID(c), channel).Update("status", status)
		if result.Error != nil {
			response.ErrorInternal(c, "更新渠道失败")
			return
		}
		if result.RowsAffected == 0 {
			response.ErrorBadRequest(c, "请先绑定该渠道")
			return
		}
		publishDataChanged(currentUserID(c), "channel", "toggled", 0, originClientID(c))
		response.Success(c, gin.H{"status": status})
	}
}

func handleUnbindChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channel := strings.ToLower(c.Param("channel"))
		if err := db.Where("user_id = ? AND channel = ?", currentUserID(c), channel).Delete(&ChannelBinding{}).Error; err != nil {
			response.ErrorInternal(c, "解绑失败")
			return
		}
		publishDataChanged(currentUserID(c), "channel", "unbound", 0, originClientID(c))
		response.Success(c, gin.H{"deleted": true})
	}
}

func handleTestChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channel := strings.ToLower(c.Param("channel"))
		if !supportedChannels[channel] {
			response.ErrorBadRequest(c, "不支持的通知渠道")
			return
		}
		userID := currentUserID(c)
		item := Reminder{ID: 0, UserID: userID, Title: "这是一条测试提醒", Notes: "渠道已经连接成功。"}
		result, err := sendChannel(c.Request.Context(), db, channel, userID, item, "test-"+strconv.FormatInt(time.Now().UnixNano(), 10), nil)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		now := time.Now()
		_ = db.Model(&ChannelBinding{}).Where("user_id = ? AND channel = ?", userID, channel).Updates(map[string]interface{}{
			"last_tested_at": &now, "last_error_code": "",
		}).Error
		response.Success(c, gin.H{"sent": true, "external_id": result.ExternalID})
	}
}

func handleCreateQQBindCode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !qqConfigured(db) {
			response.ErrorBadRequest(c, "QQ 机器人尚未开通")
			return
		}
		_ = db.Where("user_id = ?", currentUserID(c)).Delete(&QQBindCode{}).Error
		code, err := newQQBindCode()
		if err != nil {
			response.ErrorInternal(c, "生成绑定码失败")
			return
		}
		row := QQBindCode{UserID: currentUserID(c), Code: code, ExpiresAt: time.Now().Add(10 * time.Minute)}
		if err := db.Create(&row).Error; err != nil {
			response.ErrorInternal(c, "保存绑定码失败")
			return
		}
		response.Success(c, gin.H{"code": code, "expires_at": row.ExpiresAt})
	}
}

func channelStatuses(db *gorm.DB, userID uint) []ChannelStatus {
	var bindings []ChannelBinding
	_ = db.Where("user_id = ?", userID).Find(&bindings).Error
	byChannel := map[string][]ChannelBinding{}
	for _, binding := range bindings {
		byChannel[binding.Channel] = append(byChannel[binding.Channel], binding)
	}
	defs := []ChannelStatus{
		{Channel: ChannelInApp, Label: "站内消息", Configured: true, Bound: true, Status: "active", Description: "在通知中心准时提醒"},
		{Channel: ChannelEmail, Label: "电子邮件", Configured: emailConfigured(db), Description: "适合重要事项和较长内容，可绑定多个接收邮箱"},
		{Channel: ChannelSMS, Label: "手机短信", Configured: smsConfigured(db), Description: "无需打开应用即可收到"},
		{Channel: ChannelFeishu, Label: "飞书机器人", Configured: feishuConfigured(db), Description: "由企业自建应用机器人单聊提醒"},
		{Channel: ChannelQQ, Label: "QQ 机器人", Configured: qqConfigured(db), Description: "通过 QQ 机器人主动单聊提醒"},
		{Channel: ChannelDingTalk, Label: "钉钉群机器人", Configured: true, Description: "个人群自定义机器人，支持关键词或加签，无需创建钉钉应用"},
		{Channel: ChannelBark, Label: "Bark", Configured: true, Description: "推送到 iPhone 的 Bark 客户端，支持官方服务与自建服务器"},
	}
	for i := range defs {
		if defs[i].Channel == ChannelQQ {
			// This is intentionally public metadata: it lets a new user open the
			// already-configured QQ bot before sending the one-time bind code.
			if settings, err := providerSettings(db, ChannelQQ); err == nil {
				defs[i].BotLink = settings["bot_link"]
			}
		}
		list := byChannel[defs[i].Channel]
		if len(list) == 0 {
			if defs[i].Channel != ChannelInApp {
				defs[i].Status = "unbound"
			}
			continue
		}
		defs[i].Bound = true
		defs[i].Status, defs[i].TargetMasked = list[0].Status, list[0].TargetMasked
		defs[i].LastErrorCode = list[0].LastErrorCode
		for _, binding := range list {
			if binding.Status == "active" {
				defs[i].Status, defs[i].TargetMasked = binding.Status, binding.TargetMasked
				defs[i].LastErrorCode = binding.LastErrorCode
				break
			}
		}
		// Only email surfaces each target individually so the UI can add or
		// remove addresses one by one.
		if defs[i].Channel == ChannelEmail {
			for _, binding := range list {
				defs[i].Bindings = append(defs[i].Bindings, ChannelBindingItem{
					ID: binding.ID, TargetMasked: binding.TargetMasked, Status: binding.Status,
					LastErrorCode: binding.LastErrorCode,
				})
			}
		}
	}
	return defs
}

func normalizeTarget(channel, raw string) (string, error) {
	target := strings.TrimSpace(raw)
	switch channel {
	case ChannelEmail:
		addr, err := mail.ParseAddress(target)
		if err != nil || addr.Address != target {
			return "", errors.New("请输入有效的邮箱地址")
		}
	case ChannelSMS:
		target = strings.TrimPrefix(target, "+86")
		if !cnMobile.MatchString(target) {
			return "", errors.New("请输入有效的中国大陆手机号")
		}
	case ChannelFeishu:
		if target == "" || len(target) > 200 {
			return "", errors.New("请输入飞书工作邮箱、中国大陆手机号或 OpenID")
		}
	case ChannelQQ:
		if target == "" || len(target) > 200 {
			return "", errors.New("请输入有效的平台 OpenID")
		}
	case ChannelDingTalk:
		return normalizeDingTalkWebhook(target)
	case ChannelBark:
		return normalizeBarkURL(target)
	default:
		return "", errors.New("不支持的通知渠道")
	}
	return target, nil
}

func maskTarget(channel, target string) string {
	switch channel {
	case ChannelEmail:
		parts := strings.Split(target, "@")
		if len(parts) == 2 {
			name := parts[0]
			if len(name) > 2 {
				name = name[:2] + "***"
			} else {
				name += "***"
			}
			return name + "@" + parts[1]
		}
	case ChannelSMS:
		if len(target) == 11 {
			return target[:3] + "****" + target[7:]
		}
	case ChannelDingTalk:
		return "钉钉机器人 Webhook（已加密）"
	case ChannelBark:
		// 只露服务器与设备 key 的尾部，方便用户分辨是哪台设备，不泄漏完整 key。
		endpoint, err := url.Parse(target)
		if err != nil || endpoint.Host == "" {
			return "Bark 设备（已加密）"
		}
		key := strings.Trim(endpoint.Path, "/")
		if len(key) > 4 {
			key = "…" + key[len(key)-4:]
		}
		return endpoint.Host + "/" + key
	default:
		if len(target) > 10 {
			return target[:5] + "…" + target[len(target)-4:]
		}
	}
	return target
}

// normalizeDingTalkWebhook only accepts the official custom-robot endpoint.
// A user-provided webhook is an outbound-request capability, so accepting an
// arbitrary URL here would turn a personal channel binding into an SSRF sink.
func normalizeDingTalkWebhook(raw string) (string, error) {
	endpoint, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(endpoint.Scheme, "https") ||
		!strings.EqualFold(endpoint.Hostname(), "oapi.dingtalk.com") || endpoint.Port() != "" ||
		endpoint.User != nil || endpoint.Path != "/robot/send" || endpoint.Fragment != "" {
		return "", errors.New("请输入有效的钉钉自定义机器人 Webhook 地址")
	}
	query := endpoint.Query()
	tokens, ok := query["access_token"]
	if !ok || len(query) != 1 || len(tokens) != 1 || strings.TrimSpace(tokens[0]) == "" || len(tokens[0]) > 500 {
		return "", errors.New("请粘贴仅含 access_token 的钉钉机器人 Webhook 地址")
	}
	endpoint.Scheme = "https"
	endpoint.Host = "oapi.dingtalk.com"
	endpoint.RawQuery = url.Values{"access_token": []string{tokens[0]}}.Encode()
	return endpoint.String(), nil
}

// dingTalkTarget 是钉钉渠道在 ChannelBinding.Target 里保存的明文结构。
//
// 早期版本只存一个 webhook 字符串（只支持「自定义关键词」安全设置）。加签模式
// 需要额外保存密钥，于是改为 JSON；读取时解析失败就按纯 URL 处理，老绑定无需
// 迁移即可继续工作。
type dingTalkTarget struct {
	Webhook string `json:"webhook"`
	Secret  string `json:"secret,omitempty"`
}

func parseDingTalkTarget(raw string) (webhook, secret string) {
	var parsed dingTalkTarget
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil && parsed.Webhook != "" {
		return parsed.Webhook, parsed.Secret
	}
	return raw, ""
}

// signDingTalkWebhook 按钉钉自定义机器人「加签」规则追加 timestamp 与 sign：
// 以密钥为 HMAC-SHA256 的 key，对 "<毫秒时间戳>\n<密钥>" 签名后 base64 + URL 编码。
func signDingTalkWebhook(webhook, secret string) string {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	separator := "?"
	if strings.Contains(webhook, "?") {
		separator = "&"
	}
	return webhook + separator + "timestamp=" + timestamp + "&sign=" + sign
}

// normalizeBarkURL accepts a Bark push endpoint: either the official
// api.day.app/{device_key} or a self-hosted server with the same shape.
//
// 与钉钉不同，Bark 的服务器地址由用户自建、没有固定域名，所以 host 不能写死。
// 代价是这个绑定本身就是「让服务器向用户指定的地址发一次请求」的能力——与短信
// webhook 属于同一类权衡：目标由用户自己填写、只用于推送，服务端不会把响应体
// 回显给调用方。这里仍然限制协议、拒绝带 query 的地址，避免把参数拼进推送内容。
func normalizeBarkURL(raw string) (string, error) {
	endpoint, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return "", errors.New("请输入有效的 Bark 地址，例如 https://api.day.app/你的设备Key")
	}
	if endpoint.RawQuery != "" {
		return "", errors.New("Bark 地址请只填到设备 Key 为止，不要带查询参数")
	}
	path := strings.Trim(endpoint.Path, "/")
	if path == "" || len(path) > 200 {
		return "", errors.New("Bark 地址里缺少设备 Key")
	}
	return endpoint.Scheme + "://" + endpoint.Host + "/" + path, nil
}

func encryptionKey(db *gorm.DB) ([]byte, error) {
	if raw := os.Getenv("REMINDER_DATA_KEY"); raw != "" {
		sum := sha256.Sum256([]byte(raw))
		return sum[:], nil
	}
	secret, err := sysconfig.GetConfig(db, "jwt_secret", 0)
	if err != nil || secret == "" {
		return nil, errors.New("missing encryption key")
	}
	sum := sha256.Sum256([]byte("reminder-channel:" + secret))
	return sum[:], nil
}

func encryptTarget(db *gorm.DB, plain string) (string, error) {
	key, err := encryptionKey(db)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func decryptTarget(db *gorm.DB, encoded string) (string, error) {
	key, err := encryptionKey(db)
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted target")
	}
	return stringValue(gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil))
}

func stringValue(value []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return string(value), nil
}

// outboundMessage is the channel-neutral payload every sender consumes. A
// reminder and a system message (today: weather) share the delivery machinery
// but keep distinct wording, grouping and notification types.
type outboundMessage struct {
	Title            string
	Body             string
	Notes            string
	Subject          string
	Group            string
	Kind             string
	NotificationType string
	ReminderID       *uint
	DueAt            *time.Time
}

func reminderOutbound(item Reminder) outboundMessage {
	msg := outboundMessage{
		Title: item.Title, Body: notificationBody(item), Notes: item.Notes,
		Subject: "提醒：" + item.Title, Group: "提醒事项",
		Kind: "reminder", NotificationType: "reminder_due", DueAt: item.DueAt,
	}
	if item.ID > 0 {
		msg.ReminderID = &item.ID
	}
	return msg
}

func weatherOutbound(title, body string) outboundMessage {
	return outboundMessage{
		Title: title, Body: body, Subject: title, Group: "天气提醒",
		Kind: "weather", NotificationType: "weather",
	}
}

// sendChannel delivers one reminder occurrence on one channel. targetIDs
// narrows multi-target channels (email) to specific bindings; nil or empty
// delivers to every active binding.
func sendChannel(ctx context.Context, db *gorm.DB, channel string, userID uint, item Reminder, idempotencyKey string, targetIDs []uint) (sendResult, error) {
	return deliverMessage(ctx, db, channel, userID, reminderOutbound(item), idempotencyKey, targetIDs)
}

// sendTextChannel delivers a non-reminder message (for example a weather
// summary) through the same bindings and encryption path as reminders.
func sendTextChannel(ctx context.Context, db *gorm.DB, channel string, userID uint, title, body, idempotencyKey string, targetIDs []uint) (sendResult, error) {
	return deliverMessage(ctx, db, channel, userID, weatherOutbound(title, body), idempotencyKey, targetIDs)
}

func deliverMessage(ctx context.Context, db *gorm.DB, channel string, userID uint, msg outboundMessage, idempotencyKey string, targetIDs []uint) (sendResult, error) {
	if channel == ChannelInApp {
		n := Notification{UserID: userID, Type: msg.NotificationType, Title: msg.Title, Body: truncateRunes(msg.Body, 499)}
		if msg.ReminderID != nil && *msg.ReminderID > 0 {
			n.ReminderID = msg.ReminderID
		}
		if err := db.Create(&n).Error; err != nil {
			return sendResult{}, &deliveryError{Code: "INAPP_WRITE_FAILED", Message: "写入站内通知失败"}
		}
		publishNotification(n)
		return sendResult{ExternalID: strconv.FormatUint(uint64(n.ID), 10)}, nil
	}
	// Email resolves its (possibly multiple) recipients inside the switch;
	// every other channel still delivers to exactly one bound target.
	var target string
	if channel != ChannelEmail {
		var binding ChannelBinding
		if err := db.Where("user_id = ? AND channel = ? AND status = ?", userID, channel, "active").First(&binding).Error; err != nil {
			return sendResult{}, &deliveryError{Code: "CHANNEL_NOT_BOUND", Message: "该通知渠道尚未绑定或已停用", Permanent: true}
		}
		decrypted, err := decryptTarget(db, binding.Target)
		if err != nil {
			return sendResult{}, &deliveryError{Code: "TARGET_DECRYPT_FAILED", Message: "读取渠道绑定信息失败", Permanent: true}
		}
		target = decrypted
	}
	switch channel {
	case ChannelEmail:
		recipients, err := activeEmailRecipients(db, userID, targetIDs)
		if err != nil {
			return sendResult{}, &deliveryError{Code: "TARGET_DECRYPT_FAILED", Message: "读取渠道绑定信息失败", Permanent: true}
		}
		if len(recipients) == 0 {
			return sendResult{}, &deliveryError{Code: "CHANNEL_NOT_BOUND", Message: "该通知渠道尚未绑定或已停用", Permanent: true}
		}
		return sendEmail(db, recipients, msg)
	case ChannelSMS:
		return sendSMSWebhook(ctx, db, target, msg, idempotencyKey)
	case ChannelFeishu:
		return sendFeishu(ctx, db, target, msg, idempotencyKey)
	case ChannelQQ:
		return sendQQ(ctx, db, target, msg, idempotencyKey)
	case ChannelDingTalk:
		return sendDingTalk(ctx, db, target, msg)
	case ChannelBark:
		return sendBark(ctx, db, target, msg)
	default:
		return sendResult{}, &deliveryError{Code: "CHANNEL_UNSUPPORTED", Message: "不支持的通知渠道", Permanent: true}
	}
}

func notificationBody(item Reminder) string {
	if item.DueAt != nil {
		return "计划时间：" + item.DueAt.In(shanghai()).Format("01月02日 15:04")
	}
	return "你有一条新的提醒"
}

func emailConfigured(db *gorm.DB) bool {
	values, err := providerSettings(db, ChannelEmail)
	return err == nil && values["host"] != "" && values["from_address"] != ""
}
func smsConfigured(db *gorm.DB) bool {
	values, err := providerSettings(db, ChannelSMS)
	return err == nil && values["webhook_url"] != ""
}
func feishuConfigured(db *gorm.DB) bool {
	_, _, err := feishuCredentials(db)
	return err == nil
}
func qqConfigured(db *gorm.DB) bool {
	values, err := providerSettings(db, ChannelQQ)
	return err == nil && values["app_id"] != "" && values["app_secret"] != ""
}

// activeEmailRecipients decrypts the active email bindings this send should
// reach. When a per-reminder selection no longer matches any binding (the
// mailbox was unbound or disabled meanwhile), the reminder falls back to all
// active bindings instead of silently failing. The addresses receive the
// message in one SMTP transaction, matching the delivery model of one
// delivery job per reminder and channel.
func activeEmailRecipients(db *gorm.DB, userID uint, targetIDs []uint) ([]string, error) {
	bindings, err := activeEmailBindings(db, userID, targetIDs)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 && len(targetIDs) > 0 {
		bindings, err = activeEmailBindings(db, userID, nil)
		if err != nil {
			return nil, err
		}
	}
	recipients := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		target, err := decryptTarget(db, binding.Target)
		if err != nil {
			return nil, err
		}
		recipients = append(recipients, target)
	}
	return recipients, nil
}

func activeEmailBindings(db *gorm.DB, userID uint, targetIDs []uint) ([]ChannelBinding, error) {
	var bindings []ChannelBinding
	q := db.Where("user_id = ? AND channel = ? AND status = ?", userID, ChannelEmail, "active")
	if len(targetIDs) > 0 {
		q = q.Where("id IN ?", targetIDs)
	}
	err := q.Find(&bindings).Error
	return bindings, err
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

func safeError(err error) string {
	// 按字符而不是字节截断：错误信息会写进投递记录与通知正文，
	// 按字节截会在中文上切出半个 UTF-8 字符，JSON 里变成乱码。
	return truncateRunes(err.Error(), 160)
}

func shanghai() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}
