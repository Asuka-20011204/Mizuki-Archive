package controller

import (
	"context"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// httpJobStore 为 HTTP 测试提供最小任务仓储，重点验证请求校验和状态返回而非 SQL。
type httpJobStore struct {
	jobs   map[string]model.ProcessingJob
	assets map[string]model.DerivedAsset
}

// FindReusableProcessingJob 返回当前资料已有的未失败任务。
func (store *httpJobStore) FindReusableProcessingJob(_ context.Context, resourceID, jobType, sourceSHA256 string) (model.ProcessingJob, error) {
	for _, job := range store.jobs {
		if job.ResourceID == resourceID && job.Type == jobType && job.SourceSHA256 == sourceSHA256 && job.Status != model.ProcessingStatusFailed {
			return job, nil
		}
	}
	return model.ProcessingJob{}, repository.ErrNotFound
}

// CreateProcessingJob 保存 HTTP 测试创建的任务。
func (store *httpJobStore) CreateProcessingJob(_ context.Context, job model.ProcessingJob) error {
	store.jobs[job.ID] = job
	return nil
}

// GetProcessingJob 返回 HTTP 测试任务详情。
func (store *httpJobStore) GetProcessingJob(_ context.Context, id string) (model.ProcessingJob, error) {
	job, ok := store.jobs[id]
	if !ok {
		return model.ProcessingJob{}, repository.ErrNotFound
	}
	if asset, exists := store.assets[id]; exists {
		job.Asset = &asset
	}
	return job, nil
}

// ListProcessingJobs 返回 HTTP 测试资料的全部任务。
func (store *httpJobStore) ListProcessingJobs(_ context.Context, resourceID string) ([]model.ProcessingJob, error) {
	result := make([]model.ProcessingJob, 0)
	for _, job := range store.jobs {
		if job.ResourceID == resourceID {
			result = append(result, job)
		}
	}
	return result, nil
}

// ClaimNextProcessingJob 让测试 Worker 可领取一个任务，当前 HTTP 用例不会调用它。
func (store *httpJobStore) ClaimNextProcessingJob(_ context.Context, _ time.Time) (model.ProcessingJob, error) {
	return model.ProcessingJob{}, repository.ErrNoPendingJob
}

// CompleteProcessingJob 保存测试派生产物并完成任务。
func (store *httpJobStore) CompleteProcessingJob(_ context.Context, id, leaseToken string, asset model.DerivedAsset) error {
	job, ok := store.jobs[id]
	if !ok || job.LeaseToken != leaseToken {
		return repository.ErrJobLeaseLost
	}
	job.Status = model.ProcessingStatusSucceeded
	store.jobs[id] = job
	store.assets[asset.ID] = asset
	return nil
}

// FailProcessingJob 保存测试失败摘要并结束任务。
func (store *httpJobStore) FailProcessingJob(_ context.Context, id, leaseToken, message string, _ *time.Time) error {
	job, ok := store.jobs[id]
	if !ok || job.LeaseToken != leaseToken {
		return repository.ErrJobLeaseLost
	}
	job.Status = model.ProcessingStatusFailed
	job.LastError = message
	store.jobs[id] = job
	return nil
}

// GetDerivedAsset 返回 HTTP 测试派生产物。
func (store *httpJobStore) GetDerivedAsset(_ context.Context, id string) (model.DerivedAsset, error) {
	asset, ok := store.assets[id]
	if !ok {
		return model.DerivedAsset{}, repository.ErrNotFound
	}
	return asset, nil
}

// mustTestPasswordHash 生成 HTTP 测试专用密码哈希，避免把真实凭据写入测试代码。
func mustTestPasswordHash(t *testing.T) []byte {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// processingTestServer 构造挂载任务路由的独立 HTTP 服务，避免改变 V1 测试的共享夹具。
func processingTestServer(t *testing.T) (http.Handler, *memoryStore, *httpJobStore) {
	t.Helper()
	passwordHash := mustTestPasswordHash(t)
	store := &memoryStore{resources: map[string]model.Resource{}, sessions: map[string]time.Time{}}
	jobs := &httpJobStore{jobs: map[string]model.ProcessingJob{}, assets: map[string]model.DerivedAsset{}}
	dataDir := t.TempDir()
	resources, err := service.NewResources(store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	processing, err := service.NewProcessing(store, jobs, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := service.NewAuth(store, "owner", passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: resources, Processing: processing, Auth: auth, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	return server, store, jobs
}

// TestProcessingJobRoutes 验证手动创建、幂等返回、列表查看和非法处理类型拦截。
func TestProcessingJobRoutes(t *testing.T) {
	server, store, jobs := processingTestServer(t)
	resourceID := strings.Repeat("a", 32)
	store.resources[resourceID] = model.Resource{ID: resourceID, Name: "memo.txt", OriginalName: "memo.txt", Kind: "text", SHA256: "source"}
	cookie := login(t, server)
	request := httptest.NewRequest(http.MethodPost, "/api/resources/"+resourceID+"/jobs", strings.NewReader(`{"type":"extract_text"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:5173")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || len(jobs.jobs) != 1 {
		t.Fatalf("create returned %d: %s", response.Code, response.Body.String())
	}
	var created model.ProcessingJob
	for _, job := range jobs.jobs {
		created = job
	}
	request = httptest.NewRequest(http.MethodPost, "/api/resources/"+resourceID+"/jobs", strings.NewReader(`{"type":"extract_text"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:5173")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || len(jobs.jobs) != 1 || !strings.Contains(response.Body.String(), created.ID) {
		t.Fatalf("idempotent create returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/resources/"+resourceID+"/jobs", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), created.ID) {
		t.Fatalf("list returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/resources/"+resourceID+"/jobs", strings.NewReader(`{"type":"thumbnail"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:5173")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported type returned %d: %s", response.Code, response.Body.String())
	}
}
