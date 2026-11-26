package reminder

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

func sendEmail(db *gorm.DB, recipients []string, msg outboundMessage) (sendResult, error) {
	values, err := providerSettings(db, ChannelEmail)
	if err != nil || values["host"] == "" || values["from_address"] == "" {
		return sendResult{}, &deliveryError{Code: "PROVIDER_CONFIG_INVALID", Message: "邮件服务尚未配置", Permanent: true}
	}
	host := values["host"]
	port := values["port"]
	if port == "" {
		port = "587"
	}
	from := values["from_address"]
	name := values["from_name"]
	if name == "" {
		name = "提醒事项"
	}
	subject := msg.Subject
	body := msg.Body
	if msg.Notes != "" {
		body += "\r\n\r\n" + msg.Notes
	}
	message := []byte("From: " + name + " <" + from + ">\r\n" +
		"To: " + strings.Join(recipients, ", ") + "\r\n" +
		"Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body)
	var auth smtp.Auth
	if user := values["username"]; user != "" {
		auth = smtp.PlainAuth("", user, values["password"], host)
	}
	if err := sendSMTP(host, port, auth, from, recipients, message); err != nil {
		return sendResult{}, &deliveryError{Code: "SMTP_SEND_FAILED", Message: smtpErrorMessage(host, err)}
	}
	return sendResult{}, nil
}

func smtpErrorMessage(host string, err error) string {
	detail := strings.ToLower(err.Error())
	if strings.Contains(detail, "535") || strings.Contains(detail, "authentication failed") || strings.Contains(detail, "login fail") {
		if strings.EqualFold(host, "smtp.qq.com") {
			return "QQ 邮箱 SMTP 登录失败：请先在 QQ 邮箱“设置 → 账号”中开启 POP3/SMTP 或 IMAP/SMTP 服务，用户名填写完整邮箱，并使用新生成的授权码（不是 QQ/邮箱登录密码）"
		}
		return "SMTP 登录失败：请检查用户名，并使用邮件服务商提供的 SMTP 授权码或应用专用密码"
	}
	return "邮件发送失败：" + safeError(err)
}

// Most Chinese mailbox providers use either STARTTLS on 587 or implicit TLS
// on 465. net/smtp's SendMail covers the former only, so support both here.
func sendSMTP(host, port string, auth smtp.Auth, from string, recipients []string, message []byte) error {
	if port != "465" {
		return smtp.SendMail(host+":"+port, auth, from, recipients, message)
	}
	conn, err := tls.Dial("tcp", host+":"+port, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	defer conn.Close()
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Quit()
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

func sendSMSWebhook(ctx context.Context, db *gorm.DB, target string, msg outboundMessage, key string) (sendResult, error) {
	values, _ := providerSettings(db, ChannelSMS)
	endpoint := values["webhook_url"]
	if endpoint == "" {
		return sendResult{}, &deliveryError{Code: "PROVIDER_CONFIG_INVALID", Message: "短信服务尚未配置", Permanent: true}
	}
	payload := map[string]interface{}{
		"phone": target, "title": truncateRunes(msg.Title, 32),
		// content carries the message body (weather summaries, reminder time)
		// for gateways that render more than the title. Older gateways simply
		// ignore the extra field.
		"content": truncateRunes(msg.Body, 200), "kind": msg.Kind,
		"due_at": msg.DueAt, "idempotency_key": key,
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := postJSON(ctx, endpoint, values["webhook_token"], payload, &out); err != nil {
		return sendResult{}, err
	}
	return sendResult{ExternalID: out.ID}, nil
}

// sendDingTalk delivers to a user-owned custom group robot. The user creates
// that robot in DingTalk and pastes only its official webhook; no app ID,
// OAuth callback, or organisation-wide administrator configuration is needed.
func sendDingTalk(ctx context.Context, db *gorm.DB, target string, msg outboundMessage) (sendResult, error) {
	webhook, secret := parseDingTalkTarget(target)
	if secret != "" {
		webhook = signDingTalkWebhook(webhook, secret)
	}
	var result struct {
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := postJSON(ctx, webhook, "", map[string]interface{}{
		"msgtype": "text",
		"text":    map[string]string{"content": msg.dingTalkText(db)},
	}, &result); err != nil {
		return sendResult{}, doNotRetryExternalSend(err)
	}
	if result.ErrCode == nil || *result.ErrCode != 0 {
		code := "未知"
		if result.ErrCode != nil {
			code = strconv.Itoa(*result.ErrCode)
		}
		return sendResult{}, &deliveryError{
			Code:      "DINGTALK_SEND_FAILED",
			Message:   "钉钉机器人拒绝了消息，请检查机器人仍在群内、Webhook 是否完整，并确认安全设置与这里填写的一致（关键词“提醒”或加签密钥；错误码：" + code + "）",
			Permanent: true,
		}
	}
	return sendResult{}, nil
}

// sendBark pushes to an iPhone Bark client. The classic endpoint is
// GET {server}/{device_key}/{title}/{body}; the device key is already part of
// the stored base URL, so only title and body need escaping.
//
// 用 GET 而不是 POST：这是 Bark 唯一在官方服务与各版本自建服务上都成立的
// 推送格式。正文因此必须控制长度——提醒标题与通知正文本来就短，这里再截断
// 一次，避免中文经 URL 编码后把地址撑到服务器拒绝的长度。
func sendBark(ctx context.Context, db *gorm.DB, base string, msg outboundMessage) (sendResult, error) {
	title := notificationBrand(db)
	body := truncateRunes(msg.Title, 100)
	if msg.Body != "" {
		body += "\n" + msg.Body
	}
	if msg.Notes != "" {
		body += "\n" + truncateRunes(msg.Notes, 200)
	}
	query := url.Values{}
	group := msg.Group
	if group == "" {
		group = "提醒事项"
	}
	query.Set("group", group)
	query.Set("isArchive", "1")
	endpoint := strings.TrimSuffix(base, "/") + "/" +
		url.PathEscape(title) + "/" + url.PathEscape(body) + "?" + query.Encode()

	var result struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
	}
	if err := getJSON(ctx, endpoint, &result); err != nil {
		return sendResult{}, err
	}
	// Bark 用 200 表示成功；缺 code 字段时按成功处理（部分自建版本只回 message）。
	if result.Code != nil && *result.Code != 200 {
		return sendResult{}, &deliveryError{
			Code:      "BARK_SEND_FAILED",
			Message:   "Bark 拒绝了推送，请检查设备 Key 是否正确、Bark App 是否允许通知（返回：" + truncateRunes(result.Message, 80) + "）",
			Permanent: true,
		}
	}
	return sendResult{}, nil
}

func sendFeishu(ctx context.Context, db *gorm.DB, openID string, msg outboundMessage, idempotencyKey string) (sendResult, error) {
	base, token, err := feishuTenantAccessToken(ctx, db)
	if err != nil {
		return sendResult{}, err
	}
	content, _ := json.Marshal(map[string]string{"text": msg.robotText(db)})
	var sendResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	// Feishu uses uuid to de-duplicate a repeated submission of one message;
	// its API limits this field to 50 characters.
	if len(idempotencyKey) > 50 {
		idempotencyKey = idempotencyKey[:50]
	}
	endpoint := base + "/open-apis/im/v1/messages?receive_id_type=open_id&uuid=" + url.QueryEscape(idempotencyKey)
	if err := postJSON(ctx, endpoint, "Bearer "+token, map[string]interface{}{
		"receive_id": openID, "msg_type": "text", "content": string(content),
	}, &sendResp); err != nil {
		return sendResult{}, doNotRetryExternalSend(err)
	}
	if sendResp.Code != 0 {
		permanent := sendResp.Code >= 230000 && sendResp.Code < 240000
		return sendResult{}, &deliveryError{Code: "FEISHU_SEND_FAILED", Message: "飞书发送失败：" + sendResp.Msg, Permanent: permanent}
	}
	return sendResult{ExternalID: sendResp.Data.MessageID}, nil
}

func resolveFeishuOpenID(ctx context.Context, db *gorm.DB, identifier string) (string, error) {
	identifier = strings.TrimSpace(identifier)
	if strings.HasPrefix(identifier, "ou_") {
		return identifier, nil
	}

	var emails, mobiles []string
	if addr, err := mail.ParseAddress(identifier); err == nil && addr.Address == identifier {
		emails = []string{identifier}
	} else {
		mobile := strings.TrimPrefix(identifier, "+86")
		if !cnMobile.MatchString(mobile) {
			return "", errors.New("请输入飞书工作邮箱、中国大陆手机号或 OpenID")
		}
		mobiles = []string{mobile}
	}

	base, token, err := feishuTenantAccessToken(ctx, db)
	if err != nil {
		return "", err
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			UserList []struct {
				UserID string `json:"user_id"`
			} `json:"user_list"`
		} `json:"data"`
	}
	endpoint := base + "/open-apis/contact/v3/users/batch_get_id?user_id_type=open_id"
	if err := postJSON(ctx, endpoint, "Bearer "+token, map[string]interface{}{"emails": emails, "mobiles": mobiles}, &result); err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", &deliveryError{Code: "FEISHU_USER_LOOKUP_FAILED", Message: "无法查询飞书用户：" + result.Msg, Permanent: true}
	}
	if len(result.Data.UserList) != 1 || !strings.HasPrefix(result.Data.UserList[0].UserID, "ou_") {
		return "", &deliveryError{Code: "FEISHU_USER_NOT_FOUND", Message: "未找到该飞书用户，请确认应用可见范围和通讯录权限", Permanent: true}
	}
	return result.Data.UserList[0].UserID, nil
}

func feishuTenantAccessToken(ctx context.Context, db *gorm.DB) (string, string, error) {
	appID, appSecret, err := feishuCredentials(db)
	if err != nil {
		return "", "", &deliveryError{Code: "PROVIDER_CONFIG_INVALID", Message: "飞书服务尚未由管理员开通", Permanent: true}
	}
	base := strings.TrimRight(os.Getenv("FEISHU_BASE_URL"), "/")
	if base == "" {
		base = "https://open.feishu.cn"
	}
	var tokenResp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := postJSON(ctx, base+"/open-apis/auth/v3/tenant_access_token/internal", "", map[string]string{
		"app_id": appID, "app_secret": appSecret,
	}, &tokenResp); err != nil {
		return "", "", err
	}
	if tokenResp.Code != 0 || tokenResp.TenantAccessToken == "" {
		return "", "", &deliveryError{Code: "FEISHU_TOKEN_FAILED", Message: "获取飞书访问凭证失败：" + tokenResp.Msg}
	}
	return base, tokenResp.TenantAccessToken, nil
}

// Database settings take priority so an administrator can finish setup from
// the app. Environment variables remain a backwards-compatible deployment
// option for automated installations.
func feishuCredentials(db *gorm.DB) (string, string, error) {
	var row ProviderConfig
	if db != nil {
		if err := db.Where("provider = ?", ChannelFeishu).First(&row).Error; err == nil {
			plain, decErr := decryptTarget(db, row.Secret)
			if decErr != nil {
				return "", "", decErr
			}
			parts := strings.SplitN(plain, "\n", 2)
			if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
				return parts[0], parts[1], nil
			}
		}
	}
	appID, appSecret := os.Getenv("FEISHU_APP_ID"), os.Getenv("FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		return "", "", errors.New("feishu not configured")
	}
	return appID, appSecret, nil
}

func saveProviderSettings(db *gorm.DB, provider string, values map[string]string) error {
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	secret, err := encryptTarget(db, string(raw))
	if err != nil {
		return err
	}
	row := ProviderConfig{Provider: provider, Secret: secret}
	return db.Where("provider = ?", provider).Assign(map[string]interface{}{"secret": secret}).FirstOrCreate(&row).Error
}

func providerSettings(db *gorm.DB, provider string) (map[string]string, error) {
	if db != nil {
		var row ProviderConfig
		if err := db.Where("provider = ?", provider).First(&row).Error; err == nil {
			plain, decErr := decryptTarget(db, row.Secret)
			if decErr != nil {
				return nil, decErr
			}
			values := map[string]string{}
			if err := json.Unmarshal([]byte(plain), &values); err == nil {
				return values, nil
			}
		}
	}
	values := map[string]string{}
	switch provider {
	case ChannelEmail:
		values = map[string]string{"host": os.Getenv("SMTP_HOST"), "port": os.Getenv("SMTP_PORT"), "from_address": os.Getenv("SMTP_FROM_ADDRESS"), "from_name": os.Getenv("SMTP_FROM_NAME"), "username": os.Getenv("SMTP_USERNAME"), "password": os.Getenv("SMTP_PASSWORD")}
	case ChannelSMS:
		values = map[string]string{"webhook_url": os.Getenv("SMS_WEBHOOK_URL"), "webhook_token": os.Getenv("SMS_WEBHOOK_TOKEN")}
	case ChannelQQ:
		values = map[string]string{"app_id": os.Getenv("QQ_BOT_APP_ID"), "app_secret": os.Getenv("QQ_BOT_APP_SECRET"), "api_base": os.Getenv("QQ_BOT_API_BASE")}
	case qweatherProvider:
		values = map[string]string{"host": os.Getenv("QWEATHER_API_HOST"), "api_key": os.Getenv("QWEATHER_API_KEY")}
	}
	return values, nil
}

func notificationBrand(db *gorm.DB) string {
	values, err := providerSettings(db, "notification_brand")
	if err == nil && strings.TrimSpace(values["name"]) != "" {
		return strings.TrimSpace(values["name"])
	}
	return "提醒事项"
}

// robotText renders the message used by Feishu and QQ. Reminders keep the
// clock marker; weather summaries stay plain so they do not read as a task.
func (m outboundMessage) robotText(db *gorm.DB) string {
	prefix := "【" + notificationBrand(db) + "】\n"
	if m.Kind == "weather" {
		text := prefix + m.Title + "\n" + m.Body
		if m.Notes != "" {
			text += "\n" + m.Notes
		}
		return text
	}
	text := prefix + "⏰ " + m.Title + "\n" + m.Body
	if m.Notes != "" {
		text += "\n" + m.Notes
	}
	return text
}

// DingTalk custom robots can use a simple keyword safety rule. Every reminder
// deliberately includes “提醒”, allowing a user to set that exact keyword
// without managing a signing secret; weather titles carry it as well.
func (m outboundMessage) dingTalkText(db *gorm.DB) string {
	prefix := "【" + notificationBrand(db) + "】\n"
	if m.Kind == "weather" {
		text := prefix + m.Title + "\n" + m.Body
		if m.Notes != "" {
			text += "\n" + m.Notes
		}
		return text
	}
	text := prefix + "⏰ 提醒：" + m.Title + "\n" + m.Body
	if m.Notes != "" {
		text += "\n" + m.Notes
	}
	return text
}

// A timeout or 5xx may occur after the robot has accepted a message. Retrying
// would then deliver duplicate reminders, so robot sends are deliberately
// recorded as a single attempt and shown as a visible channel error instead.
func doNotRetryExternalSend(err error) error {
	var classified *deliveryError
	if errors.As(err, &classified) {
		return &deliveryError{Code: classified.Code, Message: classified.Message + "（为避免重复发送，未自动重试）", Permanent: true}
	}
	return &deliveryError{Code: "PROVIDER_SEND_UNCERTAIN", Message: "机器人发送结果不确定（为避免重复发送，未自动重试）", Permanent: true}
}

func sendQQ(ctx context.Context, db *gorm.DB, openID string, msg outboundMessage, _ string) (sendResult, error) {
	values, err := providerSettings(db, ChannelQQ)
	if err != nil || values["app_id"] == "" || values["app_secret"] == "" {
		return sendResult{}, &deliveryError{Code: "PROVIDER_CONFIG_INVALID", Message: "QQ 机器人尚未配置", Permanent: true}
	}
	token, err := qqAccessToken(ctx, values)
	if err != nil {
		return sendResult{}, err
	}
	result, err := sendQQMessage(ctx, token, values, openID, msg.robotText(db))
	if err != nil {
		return sendResult{}, doNotRetryExternalSend(err)
	}
	return result, nil
}

func qqAccessToken(ctx context.Context, values map[string]string) (string, error) {
	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   string `json:"expires_in"`
	}
	if err := postJSON(ctx, "https://bots.qq.com/app/getAppAccessToken", "", map[string]string{
		"appId": values["app_id"], "clientSecret": values["app_secret"],
	}, &tokenResp); err != nil {
		return "", err
	}
	if tokenResp.AccessToken == "" {
		return "", &deliveryError{Code: "QQ_TOKEN_FAILED", Message: "获取 QQ 机器人访问凭证失败"}
	}
	return tokenResp.AccessToken, nil
}

func sendQQText(ctx context.Context, token string, values map[string]string, openID, content string) error {
	_, err := sendQQMessage(ctx, token, values, openID, content)
	return err
}

func sendQQMessage(ctx context.Context, token string, values map[string]string, openID, content string) (sendResult, error) {
	base := strings.TrimRight(values["api_base"], "/")
	if base == "" {
		base = "https://api.bot.qq.com"
	}
	endpoint := base + "/v2/users/" + url.PathEscape(openID) + "/messages"
	var out struct {
		ID string `json:"id"`
	}
	if err := postJSON(ctx, endpoint, "QQBot "+token, map[string]interface{}{
		"content": content, "msg_type": 0,
	}, &out); err != nil {
		return sendResult{}, err
	}
	return sendResult{ExternalID: out.ID}, nil
}

func postJSON(ctx context.Context, endpoint, token string, payload, out interface{}) error {
	return doJSON(ctx, http.MethodPost, endpoint, token, payload, out)
}

// getJSON 用于只接受 GET 的推送接口（Bark 的 /{key}/{title}/{body} 就是这种）。
func getJSON(ctx context.Context, endpoint string, out interface{}) error {
	return doJSON(ctx, http.MethodGet, endpoint, "", nil, out)
}

func doJSON(ctx context.Context, method, endpoint, token string, payload, out interface{}) error {
	var bodyReader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		if strings.Contains(token, " ") {
			req.Header.Set("Authorization", token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &deliveryError{Code: "PROVIDER_UNAVAILABLE", Message: "通知服务暂时不可用：" + safeError(err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		permanent := resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests
		return &deliveryError{Code: "PROVIDER_HTTP_" + strconv.Itoa(resp.StatusCode), Message: fmt.Sprintf("通知服务返回 %d", resp.StatusCode), Permanent: permanent}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return &deliveryError{Code: "PROVIDER_RESPONSE_INVALID", Message: "通知服务响应格式无效"}
		}
	}
	return nil
}
