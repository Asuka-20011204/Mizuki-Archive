package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// openMultiInstanceDatabase 打开显式指定的隔离 MySQL 库，供两个模拟 API 实例共享。
func openMultiInstanceDatabase(t *testing.T) (*gorm.DB, *repository.MySQL) {
	t.Helper()
	dsn := os.Getenv("MIZUKI_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("设置 MIZUKI_TEST_MYSQL_DSN 指向隔离测试库后运行")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(config.DBName, "mizuki_test_") {
		t.Fatal("测试 DSN 必须指向 mizuki_test_ 前缀的独立数据库")
	}
	config.ParseTime = true
	config.Loc = time.UTC
	database, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDatabase.Close() })
	store := repository.NewMySQL(database)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("建立测试表失败: %v", err)
	}
	return database, store
}

// instanceRequest 发送带同源校验的 HTTP 请求，模拟某一个 API 实例接收浏览器流量。
func instanceRequest(t *testing.T, server http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
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

// TestMultiAPIInstancesShareMySQLSessionAndOwnership 验证两个无状态 API 实例共享会话与资料归属状态。
func TestMultiAPIInstancesShareMySQLSessionAndOwnership(t *testing.T) {
	database, store := openMultiInstanceDatabase(t)
	password := "correct horse battery staple"
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	username := "instance-" + time.Now().UTC().Format("150405.000000000")
	admin, err := store.EnsureAdminUser(context.Background(), username, passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeOwnership(context.Background(), admin.ID); err != nil {
		t.Fatal(err)
	}
	resourceID := strings.Repeat("9", 32)
	resource := model.Resource{ID: resourceID, Name: "shared.txt", OriginalName: "shared.txt", Kind: "text", MIME: "text/plain", Size: 6, SHA256: strings.Repeat("a", 64), StorageKey: resourceID, CreatedAt: time.Now().UTC()}
	userContext := repository.WithUserID(context.Background(), admin.ID)
	if err := store.SaveResource(userContext, resource); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Exec("DELETE FROM resources WHERE id = ?", resourceID).Error
		_ = database.Exec("DELETE FROM users WHERE id = ?", admin.ID).Error
	})
	dataDir := t.TempDir()
	resourcesA, err := service.NewResources(store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	resourcesB, err := service.NewResources(store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	authA, err := service.NewAuth(store, username, passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	authA.SetUserID(admin.ID)
	authB, err := service.NewAuth(store, username, passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	authB.SetUserID(admin.ID)
	serverA, err := New(Config{Resources: resourcesA, Auth: authA, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	serverB, err := New(Config{Resources: resourcesB, Auth: authB, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	loginResponse := instanceRequest(t, serverA, http.MethodPost, "/api/login", `{"username":"`+username+`","password":"`+password+`"}`, nil)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("instance A login failed: status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("instance A did not issue a session cookie")
	}
	cookie := cookies[0]
	meResponse := instanceRequest(t, serverB, http.MethodGet, "/api/me", "", cookie)
	if meResponse.Code != http.StatusOK || !strings.Contains(meResponse.Body.String(), username) {
		t.Fatalf("instance B could not validate instance A session: status=%d body=%s", meResponse.Code, meResponse.Body.String())
	}
	listResponse := instanceRequest(t, serverB, http.MethodGet, "/api/resources", "", cookie)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), resourceID) {
		t.Fatalf("instance B could not read shared user resource: status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
}
