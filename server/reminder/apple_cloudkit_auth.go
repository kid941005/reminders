package reminder

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/pbkdf2"

	"smallgo/server/reminder/applesrp"
)

const (
	appleCloudKitAuthEndpoint  = "https://idmsa.apple.com/appleauth/auth"
	appleCloudKitSetupEndpoint = "https://setup.icloud.com/setup/ws/1"
	appleCloudKitHomeEndpoint  = "https://www.icloud.com"
	appleCloudKitWidgetKey     = "d39ba9916b7251055b22c7f910e2ea796ee65e98b2ddecea8f5dde8d9d1a815d"
	appleCloudKitLoginTTL      = 30 * time.Minute
)

var appleCloudKitAuthDomains = []string{
	"https://idmsa.apple.com",
	"https://appleid.apple.com",
	"https://www.icloud.com",
	"https://setup.icloud.com",
	"https://www.apple.com",
}

type appleCloudKitCookie struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Domain  string `json:"domain"`
	Path    string `json:"path"`
	Expires int64  `json:"expires"`
	Secure  bool   `json:"secure"`
}

// appleCloudKitSession is the persisted authentication state for CloudKit.
// It is encrypted at rest and never returned to the browser.
type appleCloudKitSession struct {
	AppleID        string                `json:"apple_id,omitempty"`
	CKBaseURL      string                `json:"ck_base_url"`
	SessionToken   string                `json:"session_token,omitempty"`
	TrustToken     string                `json:"trust_token,omitempty"`
	AccountCountry string                `json:"account_country,omitempty"`
	SessionID      string                `json:"session_id,omitempty"`
	Scnt           string                `json:"scnt,omitempty"`
	DSID           string                `json:"dsid,omitempty"`
	Cookies        []appleCloudKitCookie `json:"cookies"`
	CreatedAt      string                `json:"created_at,omitempty"`
}

type appleCloudKitAuth struct {
	username   string
	password   string
	clientID   string
	frameID    string
	authAttr   string
	sessionID  string
	scnt       string
	dsID       string
	authToken  string
	trustToken string
	jar        *cookiejar.Jar
	client     *http.Client
}

type appleCloudKitAuthInitResp struct {
	Iteration int    `json:"iteration"`
	Salt      string `json:"salt"`
	Protocol  string `json:"protocol"`
	B         string `json:"b"`
	C         string `json:"c"`
}

func newAppleCloudKitAuth(username, password string) *appleCloudKitAuth {
	jar, _ := cookiejar.New(nil)
	frameID := strings.ToLower(uuid.New().String())
	return &appleCloudKitAuth{
		username: username,
		password: password,
		clientID: "auth-" + frameID,
		frameID:  frameID,
		jar:      jar,
		client:   &http.Client{Jar: jar, Timeout: 45 * time.Second},
	}
}

// appleCloudKitLoginState holds either an in-progress 2FA challenge or a
// completed session waiting for the user to pick a list.
type appleCloudKitLoginState struct {
	auth      *appleCloudKitAuth
	session   *appleCloudKitSession
	createdAt time.Time
}

var appleCloudKitLogins sync.Map

func storeAppleCloudKitLogin(state *appleCloudKitLoginState) string {
	// Opportunistically drop expired login states so repeated login attempts do
	// not grow this in-memory map without bound.
	appleCloudKitLogins.Range(func(key, value interface{}) bool {
		if st, ok := value.(*appleCloudKitLoginState); ok && time.Since(st.createdAt) > appleCloudKitLoginTTL {
			appleCloudKitLogins.Delete(key)
		}
		return true
	})
	loginID := uuid.NewString()
	state.createdAt = time.Now()
	appleCloudKitLogins.Store(loginID, state)
	return loginID
}

func loadAppleCloudKitLogin(loginID string) (*appleCloudKitLoginState, error) {
	value, ok := appleCloudKitLogins.Load(strings.TrimSpace(loginID))
	if !ok {
		return nil, errors.New("登录会话已失效，请重新登录")
	}
	state, _ := value.(*appleCloudKitLoginState)
	if state == nil || time.Since(state.createdAt) > appleCloudKitLoginTTL {
		appleCloudKitLogins.Delete(loginID)
		return nil, errors.New("登录会话已过期，请重新登录")
	}
	return state, nil
}

func takeAppleCloudKitSession(loginID string) (*appleCloudKitSession, error) {
	state, err := loadAppleCloudKitLogin(loginID)
	if err != nil {
		return nil, err
	}
	if state.session == nil {
		return nil, errors.New("请先完成两步验证")
	}
	return state.session, nil
}

// startAppleCloudKitLogin runs the password/SRP phase. When Apple requires a
// verification code it returns need2FA=true and a login_id the browser uses
// for the verify call.
func startAppleCloudKitLogin(appleID, password string) (need2FA bool, loginID string, session *appleCloudKitSession, err error) {
	appleID = strings.TrimSpace(appleID)
	password = strings.TrimSpace(password)
	if appleID == "" {
		return false, "", nil, errors.New("请填写 Apple ID")
	}
	if password == "" {
		return false, "", nil, errors.New("请填写 Apple 密码或 App 专用密码")
	}
	auth := newAppleCloudKitAuth(appleID, password)
	if err := auth.authStart(); err != nil {
		return false, "", nil, fmt.Errorf("连接 Apple 登录服务失败: %w", err)
	}
	if err := auth.authFederate(); err != nil {
		return false, "", nil, err
	}
	needs2FA, err := auth.srpAuth()
	if err != nil {
		return false, "", nil, err
	}
	if needs2FA {
		loginID = storeAppleCloudKitLogin(&appleCloudKitLoginState{auth: auth})
		return true, loginID, nil, nil
	}
	session, err = auth.finishSession()
	if err != nil {
		return false, "", nil, err
	}
	loginID = storeAppleCloudKitLogin(&appleCloudKitLoginState{session: session})
	return false, loginID, session, nil
}

// verifyAppleCloudKitLogin submits the 6-digit code and completes the login.
func verifyAppleCloudKitLogin(loginID, code string) (*appleCloudKitSession, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("请输入 6 位验证码")
	}
	state, err := loadAppleCloudKitLogin(loginID)
	if err != nil {
		return nil, err
	}
	if state.auth == nil {
		if state.session == nil {
			return nil, errors.New("登录状态无效，请重新登录")
		}
		return state.session, nil
	}
	if err := state.auth.submitTwoFactor(code); err != nil {
		return nil, err
	}
	session, err := state.auth.finishSession()
	if err != nil {
		return nil, err
	}
	appleCloudKitLogins.Store(strings.TrimSpace(loginID), &appleCloudKitLoginState{session: session, createdAt: time.Now()})
	return session, nil
}

func (a *appleCloudKitAuth) finishSession() (*appleCloudKitSession, error) {
	if err := a.getTrust(); err != nil {
		// Trust tokens are optional; accountLogin can still succeed with the
		// session token/cookies.
		_ = err
	}
	ckURL, err := a.accountLogin()
	if err != nil {
		return nil, fmt.Errorf("登录 iCloud 成功但获取 CloudKit 地址失败: %w", err)
	}
	return &appleCloudKitSession{
		AppleID:      a.username,
		CKBaseURL:    ckURL,
		SessionToken: a.authToken,
		TrustToken:   a.trustToken,
		SessionID:    a.sessionID,
		Scnt:         a.scnt,
		DSID:         a.dsID,
		Cookies:      a.extractCookies(ckURL),
		CreatedAt:    time.Now().Format(time.RFC3339),
	}, nil
}

func (a *appleCloudKitAuth) authStart() error {
	reqURL := fmt.Sprintf("%s/authorize/signin?frame_id=%s&language=en_US&skVersion=7&iframeId=%s&client_id=%s&redirect_uri=https://www.icloud.com&response_type=code&response_mode=web_message&state=%s&authVersion=latest",
		appleCloudKitAuthEndpoint, a.clientID, a.clientID, appleCloudKitWidgetKey, a.clientID)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", appleCloudKitUserAgent)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("Apple 登录服务返回 HTTP %d", resp.StatusCode)
	}
	a.authAttr = resp.Header.Get("X-Apple-Auth-Attributes")
	return nil
}

func (a *appleCloudKitAuth) authFederate() error {
	body, _ := json.Marshal(map[string]interface{}{"accountName": a.username, "rememberMe": true})
	req, err := http.NewRequest("POST", appleCloudKitAuthEndpoint+"/federate?isRememberMeEnabled=true", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = a.updateAuthHeaders(req.Header)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("Apple ID 校验失败（HTTP %d），请确认账号格式", resp.StatusCode)
	}
	return nil
}

func (a *appleCloudKitAuth) srpAuth() (bool, error) {
	params := applesrp.GetParams(2048)
	params.NoUserNameInX = true
	client := applesrp.NewSRPClient(params, nil)
	initResp, err := a.authInit(base64.StdEncoding.EncodeToString(client.GetABytes()))
	if err != nil {
		return false, err
	}
	salt, err := base64.StdEncoding.DecodeString(initResp.Salt)
	if err != nil {
		return false, errors.New("Apple 登录响应格式异常（salt）")
	}
	bBytes, err := base64.StdEncoding.DecodeString(initResp.B)
	if err != nil {
		return false, errors.New("Apple 登录响应格式异常（B）")
	}
	passHash := sha256.Sum256([]byte(a.password))
	passKey := pbkdf2.Key(passHash[:], salt, initResp.Iteration, 32, sha256.New)
	client.ProcessClientChanllenge([]byte(a.username), passKey, salt, bBytes)
	return a.authComplete(initResp.C,
		base64.StdEncoding.EncodeToString(client.M1),
		base64.StdEncoding.EncodeToString(client.M2))
}

func (a *appleCloudKitAuth) authInit(aVal string) (*appleCloudKitAuthInitResp, error) {
	body := map[string]interface{}{
		"a":           aVal,
		"accountName": a.username,
		"protocols":   []string{"s2k", "s2k_fo"},
	}
	bodyJSON, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", appleCloudKitAuthEndpoint+"/signin/init", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header = a.updateAuthHeaders(req.Header)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Apple 登录初始化失败（HTTP %d）", resp.StatusCode)
	}
	var result appleCloudKitAuthInitResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("解析 Apple 登录响应失败: %w", err)
	}
	return &result, nil
}

func (a *appleCloudKitAuth) authComplete(c, m1, m2 string) (bool, error) {
	trustTokens := []string{}
	if a.trustToken != "" {
		trustTokens = append(trustTokens, a.trustToken)
	}
	body := map[string]interface{}{
		"accountName": a.username,
		"rememberMe":  true,
		"trustTokens": trustTokens,
		"m1":          m1,
		"c":           c,
		"m2":          m2,
	}
	bodyJSON, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", appleCloudKitAuthEndpoint+"/signin/complete?isRememberMeEnabled=true", bytes.NewReader(bodyJSON))
	if err != nil {
		return false, err
	}
	req.Header = a.updateAuthHeaders(req.Header)
	resp, err := a.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	a.sessionID = resp.Header.Get("X-Apple-ID-Session-Id")
	a.scnt = resp.Header.Get("scnt")
	switch resp.StatusCode {
	case 200:
		return false, nil
	case 409:
		return true, nil
	case 403:
		return false, errors.New("Apple ID 或密码不正确")
	case 401:
		return false, errors.New("Apple 登录未授权，请检查账号与密码")
	case 412:
		return false, errors.New("需要先在 appleid.apple.com 同意隐私条款")
	default:
		return false, fmt.Errorf("Apple 登录失败（HTTP %d）", resp.StatusCode)
	}
}

func (a *appleCloudKitAuth) submitTwoFactor(code string) error {
	bodyJSON, _ := json.Marshal(map[string]interface{}{
		"securityCode": map[string]string{"code": strings.TrimSpace(code)},
	})
	req, err := http.NewRequest("POST", appleCloudKitAuthEndpoint+"/verify/trusteddevice/securitycode", bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header = a.updateAuthHeaders(req.Header)
	req.Header.Set("X-Apple-ID-Session-Id", a.sessionID)
	req.Header.Set("scnt", a.scnt)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("两步验证码校验失败（HTTP %d），请确认验证码是否正确", resp.StatusCode)
	}
	if next := resp.Header.Get("scnt"); next != "" {
		a.scnt = next
	}
	return nil
}

func (a *appleCloudKitAuth) getTrust() error {
	req, err := http.NewRequest("GET", appleCloudKitAuthEndpoint+"/2sv/trust", nil)
	if err != nil {
		return err
	}
	req.Header = a.updateAuthHeaders(req.Header)
	req.Header.Set("X-Apple-ID-Session-Id", a.sessionID)
	req.Header.Set("scnt", a.scnt)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 204 {
		return fmt.Errorf("获取信任令牌失败（HTTP %d）", resp.StatusCode)
	}
	a.authToken = resp.Header.Get("X-Apple-Session-Token")
	a.trustToken = resp.Header.Get("X-Apple-TwoSV-Trust-Token")
	return nil
}

func (a *appleCloudKitAuth) accountLogin() (string, error) {
	token := a.authToken
	if token == "" {
		token = a.sessionTokenFromCookies()
	}
	body := map[string]interface{}{"dsWebAuthToken": token, "extended_login": true}
	bodyJSON, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", appleCloudKitSetupEndpoint+"/accountLogin", bytes.NewReader(bodyJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", appleCloudKitHomeEndpoint)
	req.Header.Set("Referer", appleCloudKitHomeEndpoint+"/")
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("获取 iCloud 会话失败（HTTP %d）", resp.StatusCode)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}
	if dsInfo, ok := result["dsInfo"].(map[string]interface{}); ok {
		if dsid, ok := dsInfo["dsid"].(string); ok {
			a.dsID = dsid
		}
	}
	webservices, ok := result["webservices"].(map[string]interface{})
	if !ok {
		return "", errors.New("iCloud 登录响应缺少 webservices")
	}
	ckws, ok := webservices["ckdatabasews"].(map[string]interface{})
	if !ok {
		return "", errors.New("iCloud 登录响应缺少 CloudKit 服务")
	}
	ckURL, ok := ckws["url"].(string)
	if !ok || strings.TrimSpace(ckURL) == "" {
		return "", errors.New("iCloud 登录响应缺少 CloudKit 地址")
	}
	return ckURL, nil
}

func (a *appleCloudKitAuth) sessionTokenFromCookies() string {
	for _, domain := range appleCloudKitAuthDomains {
		u, _ := url.Parse(domain)
		for _, cookie := range a.jar.Cookies(u) {
			if cookie.Name == "X-APPLE-DS-WEB-SESSION-TOKEN" {
				return unquoteCookieValue(cookie.Value)
			}
		}
	}
	return ""
}

func (a *appleCloudKitAuth) updateAuthHeaders(h http.Header) http.Header {
	if a.scnt != "" {
		h.Set("scnt", a.scnt)
	}
	if a.sessionID != "" {
		h.Set("X-Apple-ID-Session-Id", a.sessionID)
	}
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("Referer", "https://idmsa.apple.com/")
	h.Set("Origin", "https://idmsa.apple.com")
	h.Set("X-Apple-Widget-Key", appleCloudKitWidgetKey)
	h.Set("X-Apple-I-Require-UE", "true")
	h.Set("X-Apple-Auth-Attributes", a.authAttr)
	h.Set("User-Agent", appleCloudKitUserAgent)
	h.Set("X-Apple-Mandate-Security-Upgrade", "0")
	h.Set("X-Apple-Oauth-Client-Id", appleCloudKitWidgetKey)
	h.Set("X-Apple-Oauth-Client-Type", "firstPartyAuth")
	h.Set("X-Apple-Oauth-Redirect-URI", "https://www.icloud.com")
	h.Set("X-Apple-Oauth-Require-Grant-Code", "true")
	h.Set("X-Apple-Oauth-Response-Mode", "web_message")
	h.Set("X-Apple-Oauth-Response-Type", "code")
	h.Set("X-Apple-Oauth-State", a.clientID)
	h.Set("X-Apple-Offer-Security-Upgrade", "1")
	h.Set("X-Apple-Frame-Id", a.clientID)
	h.Set("X-Apple-I-FD-Client-Info", appleCloudKitClientInfo)
	return h
}

func (a *appleCloudKitAuth) extractCookies(extra ...string) []appleCloudKitCookie {
	seen := make(map[string]bool)
	var result []appleCloudKitCookie
	domains := append([]string{}, appleCloudKitAuthDomains...)
	for _, domain := range extra {
		if strings.TrimSpace(domain) != "" {
			domains = append(domains, domain)
		}
	}
	for _, domain := range domains {
		u, _ := url.Parse(domain)
		for _, c := range a.jar.Cookies(u) {
			key := c.Name + "|" + c.Domain + "|" + c.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			exp := int64(0)
			if !c.Expires.IsZero() {
				exp = c.Expires.Unix()
			}
			result = append(result, appleCloudKitCookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: exp, Secure: c.Secure})
		}
	}
	return result
}

func unquoteCookieValue(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}

const appleCloudKitUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

const appleCloudKitClientInfo = `{"U":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36","L":"en-US","Z":"GMT-04:00","V":"1.1","F":".ta44j1e3NlY5BNlY5BSs5uQ32SCVgdI.AqWJ4EKKw0fVD_DJhCizgzH_y3EjNklY_ia4WFL264HRe4FSr_JzC1zJ6rgNNlY5BNp55BNlan0Os5Apw.BS1"}"`
