package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/service"
)

type emailHTTPStore struct {
	*multiUserHTTPStore
	challenges map[string]model.EmailChallenge
	consumed   map[string]bool
}

// challengeKey 为测试仓储生成与数据库主键一致的邮箱和用途复合键。
func challengeKey(email, purpose string) string {
	return email + "\x00" + purpose
}

// ReserveEmailChallenge 模拟数据库对邮箱用途、重发冷却和验证码摘要的原子保存。
func (store *emailHTTPStore) ReserveEmailChallenge(_ context.Context, challenge model.EmailChallenge, now time.Time) (bool, error) {
	key := challengeKey(challenge.Email, challenge.Purpose)
	if current, exists := store.challenges[key]; exists && current.NextRequestAt.After(now) {
		return false, nil
	}
	store.challenges[key] = challenge
	store.consumed[key] = false
	return true, nil
}

// ConsumeEmailChallenge 模拟验证码摘要匹配、过期检查和一次性消费。
func (store *emailHTTPStore) ConsumeEmailChallenge(_ context.Context, email, purpose, digest string, now time.Time) (bool, error) {
	key := challengeKey(email, purpose)
	challenge, exists := store.challenges[key]
	if !exists || store.consumed[key] || !challenge.ExpiresAt.After(now) || challenge.Digest != digest {
		return false, nil
	}
	store.consumed[key] = true
	return true, nil
}

// DeleteEmailChallenge 删除 SMTP 投递失败后不应继续使用的测试验证码。
func (store *emailHTTPStore) DeleteEmailChallenge(_ context.Context, email, purpose, digest string) error {
	key := challengeKey(email, purpose)
	if challenge, exists := store.challenges[key]; exists && challenge.Digest == digest {
		delete(store.challenges, key)
		delete(store.consumed, key)
	}
	return nil
}

type emailHTTPSender struct {
	code string
}

// SendCode 记录测试验证码，模拟 SMTP 已接受投递但不打印真实验证码。
func (sender *emailHTTPSender) SendCode(_ context.Context, _ string, code string) error {
	sender.code = code
	return nil
}

// newEmailHTTPServer 构造启用邮箱验证码路由的隔离 HTTP 服务。
func newEmailHTTPServer(t *testing.T) (http.Handler, *emailHTTPSender) {
	t.Helper()
	baseStore := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	store := &emailHTTPStore{multiUserHTTPStore: baseStore, challenges: map[string]model.EmailChallenge{}, consumed: map[string]bool{}}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := service.NewAuth(store, "owner", passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	sender := &emailHTTPSender{}
	emailAuth, err := service.NewEmailAuth(store, store, auth, sender, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	resources, err := service.NewResources(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: resources, Auth: auth, EmailAuth: emailAuth, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	return server, sender
}

// emailHTTPJSONRequest 发送带同源头的邮箱认证请求，并复用真实路由的 Cookie 行为。
func emailHTTPJSONRequest(t *testing.T, server http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("Origin", "http://localhost:5173")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// sessionCookieFromResponse 读取服务端签发的 HttpOnly 会话 Cookie，模拟浏览器保存登录状态。
func sessionCookieFromResponse(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("response did not set session cookie: %s", response.Body.String())
	}
	return cookies[0]
}

// TestEmailAuthenticationHTTPFlow 验证邮箱注册、验证码一次性消费、退出和再次登录的 HTTP 闭环。
func TestEmailAuthenticationHTTPFlow(t *testing.T) {
	server, sender := newEmailHTTPServer(t)
	capabilitiesResponse := emailHTTPJSONRequest(t, server, http.MethodGet, "/api/auth/capabilities", "", nil)
	if capabilitiesResponse.Code != http.StatusOK || !strings.Contains(capabilitiesResponse.Body.String(), "email_verification") || !strings.Contains(capabilitiesResponse.Body.String(), "true") {
		t.Fatalf("email capability was not advertised: status=%d body=%s", capabilitiesResponse.Code, capabilitiesResponse.Body.String())
	}
	email := "User@Example.com"
	requestResponse := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/email/register/request", `{"email":"`+email+`"}`, nil)
	if requestResponse.Code != http.StatusAccepted || sender.code == "" {
		t.Fatalf("register code request failed: status=%d body=%s", requestResponse.Code, requestResponse.Body.String())
	}
	// 注册码不能在用户创建前当作登录码使用，且失败不得写入会话。
	beforeRegistration := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/email/login", `{"email":"`+email+`","code":"`+sender.code+`"}`, nil)
	if beforeRegistration.Code != http.StatusUnauthorized || len(beforeRegistration.Result().Cookies()) != 0 {
		t.Fatalf("registration code logged in before signup: %d %s", beforeRegistration.Code, beforeRegistration.Body.String())
	}
	invalidAccount := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/email/register", `{"email":"`+email+`","code":"`+sender.code+`","username":"bad@name","password":"correct horse battery staple"}`, nil)
	if invalidAccount.Code != http.StatusBadRequest {
		t.Fatalf("invalid username accepted: %d %s", invalidAccount.Code, invalidAccount.Body.String())
	}
	registerResponse := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/email/register", `{"email":"`+email+`","code":"`+sender.code+`","username":"emailuser","password":"correct horse battery staple"}`, nil)
	if registerResponse.Code != http.StatusOK {
		t.Fatalf("email registration failed: status=%d body=%s", registerResponse.Code, registerResponse.Body.String())
	}
	session := sessionCookieFromResponse(t, registerResponse)
	meResponse := emailHTTPJSONRequest(t, server, http.MethodGet, "/api/me", "", session)
	if meResponse.Code != http.StatusOK || !strings.Contains(meResponse.Body.String(), "emailuser") {
		t.Fatalf("registered session identity incorrect: status=%d body=%s", meResponse.Code, meResponse.Body.String())
	}
	passwordLogin := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/login", `{"username":"emailuser","password":"correct horse battery staple"}`, nil)
	if passwordLogin.Code != http.StatusOK || len(passwordLogin.Result().Cookies()) == 0 {
		t.Fatalf("ordinary user password login failed: %d %s", passwordLogin.Code, passwordLogin.Body.String())
	}
	if identity := emailHTTPJSONRequest(t, server, http.MethodGet, "/api/me", "", sessionCookieFromResponse(t, passwordLogin)); identity.Code != http.StatusOK || !strings.Contains(identity.Body.String(), "emailuser") {
		t.Fatalf("password session lost owner: %d %s", identity.Code, identity.Body.String())
	}
	if wrong := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/login", `{"username":"emailuser","password":"wrong password"}`, nil); wrong.Code != http.StatusUnauthorized || len(wrong.Result().Cookies()) != 0 {
		t.Fatalf("wrong password acquired session: %d", wrong.Code)
	}
	if unknown := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/login", `{"username":"unknownuser","password":"correct horse battery staple"}`, nil); unknown.Code != http.StatusUnauthorized || len(unknown.Result().Cookies()) != 0 {
		t.Fatalf("unknown password account acquired session: %d", unknown.Code)
	}
	reusedResponse := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/email/register", `{"email":"`+email+`","code":"`+sender.code+`","username":"emailuser","password":"correct horse battery staple"}`, nil)
	if reusedResponse.Code != http.StatusUnauthorized || !strings.Contains(reusedResponse.Body.String(), "invalid_code") {
		t.Fatalf("reused registration code was accepted: status=%d body=%s", reusedResponse.Code, reusedResponse.Body.String())
	}
	logoutResponse := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/logout", "", session)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("email logout failed: status=%d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
	oldSessionResponse := emailHTTPJSONRequest(t, server, http.MethodGet, "/api/me", "", session)
	if oldSessionResponse.Code != http.StatusUnauthorized {
		t.Fatalf("email logout did not revoke old session: status=%d body=%s", oldSessionResponse.Code, oldSessionResponse.Body.String())
	}
	loginRequestResponse := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/email/login/request", `{"email":"user@example.com"}`, nil)
	if loginRequestResponse.Code != http.StatusAccepted || sender.code == "" {
		t.Fatalf("login code request failed: status=%d body=%s", loginRequestResponse.Code, loginRequestResponse.Body.String())
	}
	loginResponse := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/email/login", `{"email":"user@example.com","code":"`+sender.code+`"}`, nil)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("email login failed: status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	loginSession := sessionCookieFromResponse(t, loginResponse)
	replayedLoginResponse := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/email/login", `{"email":"user@example.com","code":"`+sender.code+`"}`, nil)
	if replayedLoginResponse.Code != http.StatusUnauthorized || !strings.Contains(replayedLoginResponse.Body.String(), "invalid_code") {
		t.Fatalf("replayed login code was accepted: status=%d body=%s", replayedLoginResponse.Code, replayedLoginResponse.Body.String())
	}
	if meResponse = emailHTTPJSONRequest(t, server, http.MethodGet, "/api/me", "", loginSession); meResponse.Code != http.StatusOK {
		t.Fatalf("new email login session is not usable: status=%d body=%s", meResponse.Code, meResponse.Body.String())
	}
	unknownResponse := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/email/login/request", `{"email":"unknown@example.com"}`, nil)
	if unknownResponse.Code != http.StatusAccepted {
		t.Fatalf("unknown email exposed login state: status=%d body=%s", unknownResponse.Code, unknownResponse.Body.String())
	}
	// 未注册地址即使拿到其他流程的验证码，也不能获取会话或私有资料。
	unknownLogin := emailHTTPJSONRequest(t, server, http.MethodPost, "/api/email/login", `{"email":"unknown@example.com","code":"`+sender.code+`"}`, nil)
	if unknownLogin.Code != http.StatusUnauthorized || len(unknownLogin.Result().Cookies()) != 0 {
		t.Fatalf("unknown email acquired a session: status=%d body=%s", unknownLogin.Code, unknownLogin.Body.String())
	}
	if anonymous := emailHTTPJSONRequest(t, server, http.MethodGet, "/api/me", "", nil); anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("unknown email reached private profile: %d", anonymous.Code)
	}
}
