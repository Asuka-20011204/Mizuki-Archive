package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
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

// TestV7AuthenticatedListWorkload 对空的隔离 MySQL 库发起固定并发的带 Cookie 列表请求。
func TestV7AuthenticatedListWorkload(t *testing.T) {
	if os.Getenv("MIZUKI_RUN_BENCHMARK") != "1" {
		t.Skip("显式设置 MIZUKI_RUN_BENCHMARK=1 才运行负载测试")
	}
	config, err := driver.ParseDSN(os.Getenv("MIZUKI_TEST_MYSQL_DSN"))
	if err != nil || !strings.HasPrefix(config.DBName, "mizuki_test_v7_") {
		t.Fatal("负载测试仅允许独立 mizuki_test_v7_ 前缀数据库")
	}
	config.ParseTime = true
	config.Loc = time.UTC
	database, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := repository.NewMySQL(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var existing int64
	if err := database.Table("resources").Count(&existing).Error; err != nil || existing != 0 {
		t.Fatalf("负载测试库必须为空，现有资料=%d，错误=%v", existing, err)
	}
	seedV7ListResources(t, ctx, store, database)
	resources, err := service.NewResources(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("synthetic benchmark password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := service.NewAuth(store, "owner", passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(Config{Resources: resources, Auth: auth, Origin: "http://localhost:5173", Ready: connection.PingContext})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app)
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}
	loginV7Client(t, client, server.URL)
	measureV7List(t, ctx, client, server.URL)
}

// seedV7ListResources 在隔离库创建 24 条固定元数据，清理仅根据本用例生成的 ID 执行。
func seedV7ListResources(t *testing.T, ctx context.Context, store *repository.MySQL, database *gorm.DB) {
	t.Helper()
	ids := make([]string, 0, 24)
	t.Cleanup(func() { _ = database.Exec("DELETE FROM resources WHERE id IN ?", ids).Error })
	for index := 0; index < 24; index++ {
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			t.Fatal(err)
		}
		id := hex.EncodeToString(idBytes)
		name := fmt.Sprintf("v7-%02d.txt", index)
		resource := model.Resource{ID: id, Name: name, OriginalName: name, Kind: "text", MIME: "text/plain", StorageKey: id, SHA256: strings.Repeat("a", 64), Size: 128 << 10, CreatedAt: time.Now().UTC()}
		if err := store.SaveResource(ctx, resource); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
}

// loginV7Client 登录隔离实例并验证 Cookie 由客户端保管，不在测试输出中打印凭据。
func loginV7Client(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/login", strings.NewReader(`{"username":"owner","password":"synthetic benchmark password"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:5173")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("隔离登录状态=%d", response.StatusCode)
	}
}

// measureV7List 以 10 个客户端并发读取 100 次，报告总耗时、成功数及成功响应的 p50/p95。
func measureV7List(t *testing.T, ctx context.Context, client *http.Client, baseURL string) {
	t.Helper()
	const requests, concurrency = 100, 10
	latencies := make(chan time.Duration, requests)
	results := make(chan error, requests)
	jobs := make(chan struct{}, requests)
	for index := 0; index < requests; index++ {
		jobs <- struct{}{}
	}
	close(jobs)
	var workers sync.WaitGroup
	started := time.Now()
	for index := 0; index < concurrency; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range jobs {
				begin := time.Now()
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/resources?page=1", nil)
				if err != nil {
					results <- err
					continue
				}
				response, err := client.Do(request)
				if err != nil {
					results <- err
					continue
				}
				_, err = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if err != nil || response.StatusCode != http.StatusOK {
					results <- fmt.Errorf("列表响应状态=%d, 读取错误=%v", response.StatusCode, err)
					continue
				}
				latencies <- time.Since(begin)
			}
		}()
	}
	workers.Wait()
	elapsed := time.Since(started)
	close(latencies)
	close(results)
	failures := len(results)
	if failures != 0 || len(latencies) != requests {
		t.Fatalf("请求成功=%d, 失败=%d, 首个错误=%v", len(latencies), failures, <-results)
	}
	samples := make([]time.Duration, 0, requests)
	for latency := range latencies {
		samples = append(samples, latency)
	}
	sort.Slice(samples, func(left, right int) bool { return samples[left] < samples[right] })
	t.Logf("V7 auth_list requests=%d concurrency=%d elapsed_ms=%d rps=%.2f p50_ms=%.2f p95_ms=%.2f failed=%d", requests, concurrency, elapsed.Milliseconds(), float64(requests)/elapsed.Seconds(), float64(samples[49].Microseconds())/1000, float64(samples[94].Microseconds())/1000, failures)
}
