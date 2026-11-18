package reminder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	appleCloudKitContainer = "com.apple.reminders"
	appleCloudKitZone      = "Reminders"
)

type appleCloudKitAPIError struct {
	StatusCode int
	Body       string
}

func (e *appleCloudKitAPIError) Error() string {
	return fmt.Sprintf("CloudKit API 返回 HTTP %d: %s", e.StatusCode, e.Body)
}

type appleCloudKitRecordError struct {
	Code   string
	Reason string
}

func (e *appleCloudKitRecordError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("CloudKit 记录操作失败: %s", e.Code)
	}
	return fmt.Sprintf("CloudKit 记录操作失败: %s (%s)", e.Code, e.Reason)
}

type appleCloudKitClient struct {
	http    *http.Client
	ckBase  string
	ownerID string
}

func newAppleCloudKitClientFromSession(sess *appleCloudKitSession) (*appleCloudKitClient, error) {
	if sess == nil || strings.TrimSpace(sess.CKBaseURL) == "" {
		return nil, errors.New("CloudKit 会话缺少服务器地址，请重新登录 iCloud")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	var cookies []*http.Cookie
	for _, c := range sess.Cookies {
		exp := time.Time{}
		if c.Expires > 0 {
			exp = time.Unix(c.Expires, 0)
		}
		cookies = append(cookies, &http.Cookie{
			Name: c.Name, Value: unquoteCookieValue(c.Value), Domain: c.Domain,
			Path: c.Path, Expires: exp, Secure: c.Secure,
		})
	}
	setURLs := []string{
		"https://www.icloud.com",
		"https://setup.icloud.com",
		"https://idmsa.apple.com",
		"https://appleid.apple.com",
		"https://www.apple.com",
		sess.CKBaseURL,
	}
	for _, rawURL := range setURLs {
		u, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		jar.SetCookies(u, cookies)
	}
	base := strings.TrimSuffix(sess.CKBaseURL, "/") + "/"
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("CloudKit 地址无效: %w", err)
	}
	return &appleCloudKitClient{
		http:   &http.Client{Jar: jar, Timeout: 45 * time.Second},
		ckBase: base,
	}, nil
}

func newAppleCloudKitClientFromConfig(db *gorm.DB, cfg *AppleReminderSync) (*appleCloudKitClient, error) {
	if cfg == nil {
		return nil, errors.New("尚未配置 Apple 提醒事项同步")
	}
	if strings.TrimSpace(cfg.SessionData) == "" {
		return nil, errors.New("CloudKit 登录已失效，请重新登录 iCloud")
	}
	plain, err := decryptTarget(db, cfg.SessionData)
	if err != nil {
		return nil, errors.New("CloudKit 登录信息无法读取，请重新登录 iCloud")
	}
	var sess appleCloudKitSession
	if err := json.Unmarshal([]byte(plain), &sess); err != nil {
		return nil, errors.New("CloudKit 登录信息格式异常，请重新登录 iCloud")
	}
	return newAppleCloudKitClientFromSession(&sess)
}

func (c *appleCloudKitClient) post(ctx context.Context, path string, body interface{}) (map[string]interface{}, error) {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.ckBase+path, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://www.icloud.com")
	req.Header.Set("Referer", "https://www.icloud.com/")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CloudKit 请求失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusMisdirectedRequest {
		return nil, fmt.Errorf("%w: CloudKit 登录已失效，请重新登录 iCloud", errAppleAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &appleCloudKitAPIError{StatusCode: resp.StatusCode, Body: truncateCloudKitBody(string(respBody))}
	}
	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("CloudKit 响应解析失败: %w", err)
	}
	return result, nil
}

func (c *appleCloudKitClient) owner(ctx context.Context) (string, error) {
	if c.ownerID != "" {
		return c.ownerID, nil
	}
	result, err := c.post(ctx, "database/1/"+appleCloudKitContainer+"/production/private/zones/list", map[string]interface{}{})
	if err != nil {
		return "", err
	}
	zones, _ := result["zones"].([]interface{})
	for _, raw := range zones {
		zone, _ := raw.(map[string]interface{})
		zoneID, _ := zone["zoneID"].(map[string]interface{})
		if zoneID["zoneName"] == appleCloudKitZone {
			if owner, ok := zoneID["ownerRecordName"].(string); ok && owner != "" {
				c.ownerID = owner
				return owner, nil
			}
		}
	}
	if len(zones) > 0 {
		zone, _ := zones[0].(map[string]interface{})
		zoneID, _ := zone["zoneID"].(map[string]interface{})
		if owner, ok := zoneID["ownerRecordName"].(string); ok && owner != "" {
			c.ownerID = owner
			return owner, nil
		}
	}
	return "", errors.New("未找到 iCloud 提醒事项云区")
}

func (c *appleCloudKitClient) changesZone(ctx context.Context, ownerID, syncToken string) (map[string]interface{}, error) {
	spec := map[string]interface{}{
		"zoneID": map[string]interface{}{"zoneName": appleCloudKitZone, "ownerRecordName": ownerID},
		"desiredKeys": []string{
			"TitleDocument", "NotesDocument", "Name", "Completed", "CompletionDate",
			"DueDate", "List", "Deleted", "Priority", "ParentReminder",
		},
	}
	if syncToken != "" {
		spec["syncToken"] = syncToken
	}
	return c.post(ctx, "database/1/"+appleCloudKitContainer+"/production/private/changes/zone",
		map[string]interface{}{"zones": []interface{}{spec}})
}

// listReminderLists reads every ReminderList record in the CloudKit zone.
func (c *appleCloudKitClient) listReminderLists(ctx context.Context) ([]AppleReminderCollection, error) {
	ownerID, err := c.owner(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]AppleReminderCollection)
	syncToken := ""
	for page := 0; page < 20; page++ {
		result, err := c.changesZone(ctx, ownerID, syncToken)
		if err != nil {
			return nil, err
		}
		records, _ := result["records"].([]interface{})
		for _, raw := range records {
			record, _ := raw.(map[string]interface{})
			if record == nil || cloudKitDeleted(record) {
				continue
			}
			recordType, _ := record["recordType"].(string)
			if recordType != "ReminderList" && recordType != "List" {
				continue
			}
			recordName, _ := record["recordName"].(string)
			if recordName == "" {
				continue
			}
			name := cloudKitListName(record)
			if name == "" {
				name = "提醒事项"
			}
			seen[recordName] = AppleReminderCollection{URL: recordName, Name: name}
		}
		more, _ := result["moreComing"].(bool)
		if !more {
			break
		}
		next, _ := result["syncToken"].(string)
		if next == "" || next == syncToken {
			break
		}
		syncToken = next
	}
	lists := make([]AppleReminderCollection, 0, len(seen))
	for _, item := range seen {
		lists = append(lists, item)
	}
	sort.Slice(lists, func(i, j int) bool {
		if lists[i].Name == lists[j].Name {
			return lists[i].URL < lists[j].URL
		}
		return lists[i].Name < lists[j].Name
	})
	if len(lists) == 0 {
		return nil, errors.New("该 Apple ID 下没有可用的提醒事项列表，请先在 iPhone 上创建列表")
	}
	return lists, nil
}

func (c *appleCloudKitClient) modifyRecords(ctx context.Context, ownerID string, operations []map[string]interface{}) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"zoneID":     map[string]interface{}{"zoneName": appleCloudKitZone, "ownerRecordName": ownerID},
		"operations": operations,
		"atomic":     true,
	}
	result, err := c.post(ctx, "database/1/"+appleCloudKitContainer+"/production/private/records/modify", payload)
	if err != nil {
		return nil, err
	}
	if recErr := cloudKitRecordError(result); recErr != nil {
		return result, recErr
	}
	return result, nil
}

func (c *appleCloudKitClient) lookupRecord(ctx context.Context, ownerID, recordName string) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"records": []interface{}{map[string]interface{}{"recordName": recordName}},
		"zoneID":  map[string]interface{}{"zoneName": appleCloudKitZone, "ownerRecordName": ownerID},
	}
	result, err := c.post(ctx, "database/1/"+appleCloudKitContainer+"/production/private/records/lookup", payload)
	if err != nil {
		return nil, err
	}
	if recErr := cloudKitRecordError(result); recErr != nil {
		return nil, recErr
	}
	records, _ := result["records"].([]interface{})
	if len(records) == 0 {
		return nil, &appleCloudKitRecordError{Code: "NOT_FOUND"}
	}
	record, _ := records[0].(map[string]interface{})
	return record, nil
}

func cloudKitDeleted(record map[string]interface{}) bool {
	v, ok := cloudKitField(record, "Deleted")
	if !ok {
		return false
	}
	switch typed := v.(type) {
	case bool:
		return typed
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	}
	return false
}

func cloudKitListName(record map[string]interface{}) string {
	if v, ok := cloudKitField(record, "Name"); ok {
		if name, ok := v.(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	if v, ok := cloudKitField(record, "TitleDocument"); ok {
		if encoded, ok := v.(string); ok {
			if name := ckExtractTitle(encoded); name != "" {
				return name
			}
		}
	}
	return ""
}

func cloudKitField(record map[string]interface{}, key string) (interface{}, bool) {
	fields, ok := record["fields"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	field, ok := fields[key].(map[string]interface{})
	if !ok {
		return nil, false
	}
	value, ok := field["value"]
	return value, ok
}

func cloudKitRecordError(result map[string]interface{}) error {
	if result == nil {
		return nil
	}
	if code, _ := result["serverErrorCode"].(string); code != "" {
		reason, _ := result["reason"].(string)
		return &appleCloudKitRecordError{Code: code, Reason: reason}
	}
	records, _ := result["records"].([]interface{})
	for _, raw := range records {
		record, _ := raw.(map[string]interface{})
		if record == nil {
			continue
		}
		if code, _ := record["serverErrorCode"].(string); code != "" {
			reason, _ := record["reason"].(string)
			return &appleCloudKitRecordError{Code: code, Reason: reason}
		}
	}
	return nil
}

func cloudKitRecordChangeTag(record map[string]interface{}) string {
	tag, _ := record["recordChangeTag"].(string)
	return tag
}

func isAppleCloudKitRecordCode(err error, code string) bool {
	var recErr *appleCloudKitRecordError
	return errors.As(err, &recErr) && strings.EqualFold(recErr.Code, code)
}

func truncateCloudKitBody(s string) string {
	if len(s) <= 300 {
		return s
	}
	return s[:300] + "..."
}
