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
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

type phoneHTTPStore struct {
	*multiUserHTTPStore
	challenges map[string]model.PhoneChallenge
}

// CreatePhoneUser 在 HTTP 测试中模拟手机号唯一用户，不共享其他用户的资料。
func (store *phoneHTTPStore) CreatePhoneUser(_ context.Context, phone, username string, hash []byte) (model.User, error) {
	for _, user := range store.users {
		if user.Phone == phone {
			return model.User{}, repository.ErrUserExists
		}
	}
	user := model.User{ID: "phone-owner", Username: username, Phone: phone, PasswordHash: hash}
	store.users[user.ID] = user
	return user, nil
}

// GetUserByPhone 只按规范化号码查找手机号用户。
func (store *phoneHTTPStore) GetUserByPhone(_ context.Context, phone string) (model.User, error) {
	for _, user := range store.users {
		if user.Phone == phone {
			return user, nil
		}
	}
	return model.User{}, repository.ErrNotFound
}

// ReservePhoneChallenge 模拟用途隔离和单号码冷却。
func (store *phoneHTTPStore) ReservePhoneChallenge(_ context.Context, value model.PhoneChallenge, now time.Time) (bool, error) {
	key := value.Phone + ":" + value.Purpose
	if current, exists := store.challenges[key]; exists && current.NextRequestAt.After(now) {
		return false, nil
	}
	store.challenges[key] = value
	return true, nil
}

// ConsumePhoneChallenge 模拟一次性验证码和过期校验。
func (store *phoneHTTPStore) ConsumePhoneChallenge(_ context.Context, phone, purpose, digest string, now time.Time) (bool, error) {
	key := phone + ":" + purpose
	value, exists := store.challenges[key]
	if !exists || value.Digest != digest || !value.ExpiresAt.After(now) {
		return false, nil
	}
	delete(store.challenges, key)
	return true, nil
}

// DeletePhoneChallenge 仅清理本次未发送成功的挑战。
func (store *phoneHTTPStore) DeletePhoneChallenge(_ context.Context, phone, purpose, digest string) error {
	key := phone + ":" + purpose
	if store.challenges[key].Digest == digest {
		delete(store.challenges, key)
	}
	return nil
}

type phoneHTTPSender struct{ code string }

// SendCode 仅记录测试验证码，不对外发送短信。
func (sender *phoneHTTPSender) SendCode(_ context.Context, _, code string) error {
	sender.code = code
	return nil
}

// TestPhoneAuthHTTPFlow 验证条件路由、注册、重放拒绝、登录和用户归属会话。
func TestPhoneAuthHTTPFlow(t *testing.T) {
	store := &phoneHTTPStore{multiUserHTTPStore: &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}, challenges: map[string]model.PhoneChallenge{}}
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	auth, err := service.NewAuth(store, "owner", hash)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := service.NewResources(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sender := &phoneHTTPSender{}
	phoneAuth, err := service.NewPhoneAuth(store, store, auth, sender, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: resources, Auth: auth, PhoneAuth: phoneAuth, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	// post 把请求限制在与浏览器相同的 JSON 和 Origin 边界。
	post := func(path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Origin", "http://localhost:5173")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	capability := httptest.NewRecorder()
	server.ServeHTTP(capability, httptest.NewRequest(http.MethodGet, "/api/auth/capabilities", nil))
	if !strings.Contains(capability.Body.String(), `"phone_verification":true`) {
		t.Fatalf("capability: %s", capability.Body.String())
	}
	requestCode := post("/api/phone/register/request", `{"phone":"13800138000"}`)
	if requestCode.Code != http.StatusAccepted || sender.code == "" {
		t.Fatalf("register request: %d", requestCode.Code)
	}
	// 注册码不得用于未注册号码的登录，避免跳过明确的账号创建流程。
	if beforeRegistration := post("/api/phone/login", `{"phone":"13800138000","code":"`+sender.code+`"}`); beforeRegistration.Code != http.StatusUnauthorized || len(beforeRegistration.Result().Cookies()) != 0 {
		t.Fatalf("registration code logged in before signup: %d %s", beforeRegistration.Code, beforeRegistration.Body.String())
	}
	register := post("/api/phone/register", `{"phone":"+8613800138000","code":"`+sender.code+`","username":"phoneuser","password":"correct horse battery staple"}`)
	if register.Code != http.StatusOK || len(register.Result().Cookies()) == 0 {
		t.Fatalf("register: %d %s", register.Code, register.Body.String())
	}
	if replay := post("/api/phone/register", `{"phone":"13800138000","code":"`+sender.code+`","username":"phoneuser","password":"correct horse battery staple"}`); replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay: %d", replay.Code)
	}
	if unknown := post("/api/phone/login/request", `{"phone":"13900139000"}`); unknown.Code != http.StatusAccepted {
		t.Fatalf("unknown phone: %d", unknown.Code)
	}
	// 未注册号码的受理响应只为防止账号枚举，不代表可以创建登录会话。
	if unknownLogin := post("/api/phone/login", `{"phone":"13900139000","code":"`+sender.code+`"}`); unknownLogin.Code != http.StatusUnauthorized || len(unknownLogin.Result().Cookies()) != 0 {
		t.Fatalf("unknown phone acquired a session: %d %s", unknownLogin.Code, unknownLogin.Body.String())
	}
	if result := post("/api/phone/login/request", `{"phone":"13800138000"}`); result.Code != http.StatusAccepted {
		t.Fatalf("login request: %d", result.Code)
	}
	login := post("/api/phone/login", `{"phone":"13800138000","code":"`+sender.code+`"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	me := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	request.AddCookie(login.Result().Cookies()[0])
	server.ServeHTTP(me, request)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), "phoneuser") {
		t.Fatalf("owner: %d %s", me.Code, me.Body.String())
	}
	passwordLogin := post("/api/login", `{"username":"13800138000","password":"correct horse battery staple"}`)
	if passwordLogin.Code != http.StatusOK || len(passwordLogin.Result().Cookies()) == 0 {
		t.Fatalf("phone user password login: %d %s", passwordLogin.Code, passwordLogin.Body.String())
	}
}
