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

	"smallgo/server/logger"
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

type appleCloudKitTrustedDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ModelName string `json:"modelName"`
}

type appleCloudKitPhoneNumber struct {
	ID                 int    `json:"id"`
	NumberWithDialCode string `json:"numberWithDialCode"`
	PushMode           string `json:"pushMode"`
}

type appleCloudKitSecurityCodeInfo struct {
	Length                int  `json:"length"`
	TooManyCodesSent      bool `json:"tooManyCodesSent"`
	TooManyCodesValidated bool `json:"tooManyCodesValidated"`
	SecurityCodeLocked    bool `json:"securityCodeLocked"`
}

// appleCloudKitTwoFactorOptions is the subset of Apple's /appleauth/auth
// response needed to offer a verification method.
type appleCloudKitTwoFactorOptions struct {
	TrustedDevices      []appleCloudKitTrustedDevice  `json:"trustedDevices"`
	TrustedPhoneNumbers []appleCloudKitPhoneNumber    `json:"trustedPhoneNumbers"`
	NoTrustedDevices    bool                          `json:"noTrustedDevices"`
	SecurityCode        appleCloudKitSecurityCodeInfo `json:"securityCode"`
}

type appleCloudKitPhoneOption struct {
	ID     int    `json:"id"`
	Number string `json:"number"`
	Mode   string `json:"mode"`
}

type appleCloudKitTwoFactorOptionsDTO struct {
	TrustedDeviceCount int                        `json:"trusted_device_count"`
	Phones             []appleCloudKitPhoneOption `json:"phones"`
	NoTrustedDevices   bool                       `json:"no_trusted_devices"`
	CodeLength         int                        `json:"code_length"`
	TooManyCodesSent   bool                       `json:"too_many_codes_sent"`
	SecurityCodeLocked bool                       `json:"security_code_locked"`
	SMSAutoSent        bool                       `json:"sms_auto_sent"`
}

func (o *appleCloudKitTwoFactorOptions) dto() *appleCloudKitTwoFactorOptionsDTO {
	if o == nil {
		return nil
	}
	dto := &appleCloudKitTwoFactorOptionsDTO{
		TrustedDeviceCount: len(o.TrustedDevices),
		Phones:             make([]appleCloudKitPhoneOption, 0, len(o.TrustedPhoneNumbers)),
		NoTrustedDevices:   o.NoTrustedDevices,
		CodeLength:         o.SecurityCode.Length,
		TooManyCodesSent:   o.SecurityCode.TooManyCodesSent,
		SecurityCodeLocked: o.SecurityCode.SecurityCodeLocked,
	}
	if dto.CodeLength <= 0 {
		dto.CodeLength = 6
	}
	for _, phone := range o.TrustedPhoneNumbers {
		mode := strings.TrimSpace(phone.PushMode)
		if mode == "" {
			mode = "sms"
		}
		dto.Phones = append(dto.Phones, appleCloudKitPhoneOption{ID: phone.ID, Number: phone.NumberWithDialCode, Mode: mode})
	}
	dto.SMSAutoSent = o.NoTrustedDevices && len(o.TrustedPhoneNumbers) == 1
	return dto
}

// appleCloudKitMaskID keeps enough of an Apple ID to identify the account in
// logs without writing the full address.
func appleCloudKitMaskID(id string) string {
	id = strings.TrimSpace(id)
	if at := strings.IndexByte(id, '@'); at > 0 {
		local := id[:at]
		if len(local) > 2 {
			local = local[:2] + "***"
		} else {
			local = "***"
		}
		return local + id[at:]
	}
	if len(id) > 4 {
		return id[:2] + "***"
	}
	return "***"
}

// beginStep/endStep make the Apple login pipeline observable in logs: when a
// login hangs, the last "步骤开始" line names the request that never returned.
func (a *appleCloudKitAuth) beginStep(step string) time.Time {
	logger.Info("cloudkit: 步骤开始 step=%s account=%s", step, appleCloudKitMaskID(a.username))
	return time.Now()
}

func (a *appleCloudKitAuth) endStep(step string, started time.Time, err error) {
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil {
		logger.Warn("cloudkit: 步骤失败 step=%s account=%s elapsed=%s err=%v", step, appleCloudKitMaskID(a.username), elapsed, err)
		return
	}
	logger.Info("cloudkit: 步骤完成 step=%s account=%s elapsed=%s", step, appleCloudKitMaskID(a.username), elapsed)
}

func optionsTrustedDevices(o *appleCloudKitTwoFactorOptions) int {
	if o == nil {
		return 0
	}
	return len(o.TrustedDevices)
}

func optionsTrustedPhones(o *appleCloudKitTwoFactorOptions) int {
	if o == nil {
		return 0
	}
	return len(o.TrustedPhoneNumbers)
}

func optionsNoTrustedDevices(o *appleCloudKitTwoFactorOptions) bool {
	return o != nil && o.NoTrustedDevices
}

// cloudKitLoggingTransport records every request Apple's login endpoints see.
// Only method, host+path, status and Apple's request id are logged: query
// strings, bodies and cookies can carry account material and must stay out.
type cloudKitLoggingTransport struct {
	base http.RoundTripper
}

func (t cloudKitLoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now()
	logger.Info("cloudkit: 请求 %s %s", req.Method, req.URL.Host+req.URL.Path)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		logger.Warn("cloudkit: 请求失败 %s %s elapsed=%s err=%v", req.Method, req.URL.Host+req.URL.Path, time.Since(started).Round(time.Millisecond), err)
		return nil, err
	}
	logger.Info("cloudkit: 响应 %s %s status=%d request_id=%s elapsed=%s", req.Method, req.URL.Host+req.URL.Path, resp.StatusCode, resp.Header.Get("X-Apple-I-Request-Id"), time.Since(started).Round(time.Millisecond))
	return resp, nil
}

func newAppleCloudKitAuth(username, password string) *appleCloudKitAuth {
	jar, _ := cookiejar.New(nil)
	frameID := strings.ToLower(uuid.New().String())
	transport := http.DefaultTransport
	if transport == nil {
		transport = &http.Transport{}
	}
	return &appleCloudKitAuth{
		username: username,
		password: password,
		clientID: "auth-" + frameID,
		frameID:  frameID,
		jar:      jar,
		client: &http.Client{
			Jar:       jar,
			Timeout:   45 * time.Second,
			Transport: cloudKitLoggingTransport{base: transport},
		},
	}
}

// appleCloudKitLoginState holds either an in-progress 2FA challenge or a
// completed session waiting for the user to pick a list.
type appleCloudKitLoginState struct {
	auth      *appleCloudKitAuth
	session   *appleCloudKitSession
	options   *appleCloudKitTwoFactorOptions
	createdAt time.Time
	// mu serializes verify/resend on the same challenge: both mutate the
	// auth object's scnt/session headers and must not run concurrently.
	mu sync.Mutex
}

// appleCloudKitLoginStart is the outcome of the password phase. Need2FA is
// true while Apple waits for a verification code; Options describes how the
// code can be delivered.
type appleCloudKitLoginStart struct {
	Need2FA      bool
	LoginID      string
	Session      *appleCloudKitSession
	Options      *appleCloudKitTwoFactorOptions
	OptionsError string
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
func startAppleCloudKitLogin(appleID, password string) (*appleCloudKitLoginStart, error) {
	appleID = strings.TrimSpace(appleID)
	password = strings.TrimSpace(password)
	if appleID == "" {
		return nil, errors.New("请填写 Apple ID")
	}
	if password == "" {
		return nil, errors.New("请填写 Apple ID 主密码")
	}
	auth := newAppleCloudKitAuth(appleID, password)
	logger.Info("cloudkit: 登录开始 account=%s", appleCloudKitMaskID(appleID))

	started := auth.beginStep("authorize/signin")
	err := auth.authStart()
	auth.endStep("authorize/signin", started, err)
	if err != nil {
		return nil, fmt.Errorf("连接 Apple 登录服务失败: %w", err)
	}

	started = auth.beginStep("federate")
	err = auth.authFederate()
	auth.endStep("federate", started, err)
	if err != nil {
		return nil, err
	}

	started = auth.beginStep("srp")
	needs2FA, err := auth.srpAuth()
	auth.endStep("srp", started, err)
	if err != nil {
		return nil, err
	}
	if needs2FA {
		// Apple has already pushed a code to trusted devices. Fetching the
		// options lets us tell the user which devices/phones are available and
		// offer an SMS fallback when the push does not arrive.
		started = auth.beginStep("2fa/options")
		options, optionsErr := auth.twoFactorOptions()
		auth.endStep("2fa/options", started, optionsErr)
		start := &appleCloudKitLoginStart{Need2FA: true, Options: options}
		if optionsErr != nil {
			start.OptionsError = optionsErr.Error()
		}
		logger.Info("cloudkit: 等待两步验证 account=%s devices=%d phones=%d no_devices=%t options_ok=%t", appleCloudKitMaskID(appleID), optionsTrustedDevices(options), optionsTrustedPhones(options), optionsNoTrustedDevices(options), optionsErr == nil)
		start.LoginID = storeAppleCloudKitLogin(&appleCloudKitLoginState{auth: auth, options: options})
		return start, nil
	}
	started = auth.beginStep("session")
	session, err := auth.finishSession()
	auth.endStep("session", started, err)
	if err != nil {
		return nil, err
	}
	loginID := storeAppleCloudKitLogin(&appleCloudKitLoginState{session: session})
	return &appleCloudKitLoginStart{LoginID: loginID, Session: session}, nil
}

// verifyAppleCloudKitLogin submits the 6-digit code and completes the login.
// phoneID selects a trusted phone number; zero means the code pushed to a
// trusted device.
func verifyAppleCloudKitLogin(loginID, code string, phoneID int) (*appleCloudKitSession, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("请输入 6 位验证码")
	}
	state, err := loadAppleCloudKitLogin(loginID)
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.auth == nil {
		if state.session == nil {
			return nil, errors.New("登录状态无效，请重新登录")
		}
		return state.session, nil
	}
	if phoneID == 0 && state.options != nil && state.options.NoTrustedDevices {
		switch len(state.options.TrustedPhoneNumbers) {
		case 0:
			return nil, errors.New("该 Apple ID 没有受信任设备或电话号码，无法完成两步验证")
		case 1:
			// Apple sends this code automatically when there is exactly one
			// trusted phone number and no trusted device.
			phoneID = state.options.TrustedPhoneNumbers[0].ID
		default:
			return nil, errors.New("该账号没有受信任设备，请先选择电话号码并发送短信验证码")
		}
	}
	if phoneID > 0 {
		options := state.options
		if options == nil {
			started := state.auth.beginStep("2fa/options")
			options, err = state.auth.twoFactorOptions()
			state.auth.endStep("2fa/options", started, err)
			if err != nil {
				return nil, err
			}
			state.options = options
		}
		phone, err := appleCloudKitPickPhone(options, phoneID)
		if err != nil {
			return nil, err
		}
		logger.Info("cloudkit: 校验短信验证码 account=%s phone_id=%d mode=%s", appleCloudKitMaskID(state.auth.username), phone.ID, phone.PushMode)
		started := state.auth.beginStep("2fa/phone")
		err = state.auth.submitPhoneTwoFactor(phone.ID, phone.PushMode, code)
		state.auth.endStep("2fa/phone", started, err)
		if err != nil {
			return nil, err
		}
	} else {
		logger.Info("cloudkit: 校验设备验证码 account=%s", appleCloudKitMaskID(state.auth.username))
		started := state.auth.beginStep("2fa/device")
		err = state.auth.submitTwoFactor(code)
		state.auth.endStep("2fa/device", started, err)
		if err != nil {
			return nil, err
		}
	}
	started := state.auth.beginStep("session")
	session, err := state.auth.finishSession()
	state.auth.endStep("session", started, err)
	if err != nil {
		return nil, err
	}
	appleCloudKitLogins.Store(strings.TrimSpace(loginID), &appleCloudKitLoginState{session: session, createdAt: time.Now()})
	return session, nil
}

// resendAppleCloudKitLogin requests a new SMS/voice code for a trusted phone
// number. phoneID may be zero when Apple exposes exactly one phone number.
func resendAppleCloudKitLogin(loginID string, phoneID int) (*appleCloudKitPhoneOption, error) {
	state, err := loadAppleCloudKitLogin(loginID)
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.auth == nil {
		return nil, errors.New("当前登录不需要验证码")
	}
	options := state.options
	if options == nil {
		options, err = state.auth.twoFactorOptions()
		if err != nil {
			return nil, err
		}
		state.options = options
	}
	phone, err := appleCloudKitPickPhone(options, phoneID)
	if err != nil {
		return nil, err
	}
	logger.Info("cloudkit: 请求短信验证码 account=%s phone_id=%d mode=%s", appleCloudKitMaskID(state.auth.username), phone.ID, phone.PushMode)
	started := state.auth.beginStep("2fa/send")
	err = state.auth.requestPhoneCode(phone.ID, phone.PushMode)
	state.auth.endStep("2fa/send", started, err)
	if err != nil {
		return nil, err
	}
	mode := strings.TrimSpace(phone.PushMode)
	if mode == "" {
		mode = "sms"
	}
	return &appleCloudKitPhoneOption{ID: phone.ID, Number: phone.NumberWithDialCode, Mode: mode}, nil
}

func appleCloudKitPickPhone(options *appleCloudKitTwoFactorOptions, phoneID int) (*appleCloudKitPhoneNumber, error) {
	if options == nil || len(options.TrustedPhoneNumbers) == 0 {
		return nil, errors.New("该 Apple ID 没有可用于接收短信的受信任电话号码，请先在 iPhone 上登录该账号后重试")
	}
	if phoneID > 0 {
		for i := range options.TrustedPhoneNumbers {
			if options.TrustedPhoneNumbers[i].ID == phoneID {
				return &options.TrustedPhoneNumbers[i], nil
			}
		}
		return nil, errors.New("选择的电话号码已失效，请重新登录")
	}
	if len(options.TrustedPhoneNumbers) == 1 {
		return &options.TrustedPhoneNumbers[0], nil
	}
	return nil, errors.New("请选择接收验证码的电话号码")
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

// twoFactorOptions reads the verification methods Apple offers for the
// current challenge. A code has already been pushed to trusted devices.
func (a *appleCloudKitAuth) twoFactorOptions() (*appleCloudKitTwoFactorOptions, error) {
	req, err := http.NewRequest("GET", appleCloudKitAuthEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header = a.updateAuthHeaders(req.Header)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("获取两步验证方式失败（HTTP %d）", resp.StatusCode)
	}
	var options appleCloudKitTwoFactorOptions
	if err := json.Unmarshal(body, &options); err != nil {
		return nil, fmt.Errorf("解析两步验证方式失败: %w", err)
	}
	return &options, nil
}

// requestPhoneCode asks Apple to send a new code to a trusted phone number.
func (a *appleCloudKitAuth) requestPhoneCode(phoneID int, mode string) error {
	if phoneID <= 0 {
		return errors.New("请选择接收验证码的电话号码")
	}
	if strings.TrimSpace(mode) == "" {
		mode = "sms"
	}
	bodyJSON, _ := json.Marshal(map[string]interface{}{
		"phoneNumber": map[string]interface{}{"id": phoneID},
		"mode":        mode,
	})
	req, err := http.NewRequest("PUT", appleCloudKitAuthEndpoint+"/verify/phone", bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header = a.updateAuthHeaders(req.Header)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("发送短信验证码失败（HTTP %d）%s", resp.StatusCode, appleCloudKitServiceError(body))
	}
	if next := resp.Header.Get("scnt"); next != "" {
		a.scnt = next
	}
	return nil
}

// submitPhoneTwoFactor submits a code that was delivered by SMS or voice call.
func (a *appleCloudKitAuth) submitPhoneTwoFactor(phoneID int, mode, code string) error {
	if strings.TrimSpace(mode) == "" {
		mode = "sms"
	}
	bodyJSON, _ := json.Marshal(map[string]interface{}{
		"phoneNumber":  map[string]interface{}{"id": phoneID},
		"securityCode": map[string]string{"code": strings.TrimSpace(code)},
		"mode":         mode,
	})
	req, err := http.NewRequest("POST", appleCloudKitAuthEndpoint+"/verify/phone/securitycode", bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header = a.updateAuthHeaders(req.Header)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("短信验证码校验失败（HTTP %d）%s", resp.StatusCode, appleCloudKitServiceError(body))
	}
	if next := resp.Header.Get("scnt"); next != "" {
		a.scnt = next
	}
	return nil
}

// appleCloudKitServiceError extracts Apple's user-facing service error text
// without echoing the whole response body.
func appleCloudKitServiceError(body []byte) string {
	var payload struct {
		ServiceErrors []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"serviceErrors"`
		ServiceErrorsSnake []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"service_errors"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	items := payload.ServiceErrors
	if len(items) == 0 {
		items = payload.ServiceErrorsSnake
	}
	for _, item := range items {
		if strings.TrimSpace(item.Message) != "" {
			return "：" + strings.TrimSpace(item.Message)
		}
	}
	return ""
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
