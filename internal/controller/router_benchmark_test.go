package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// countingRateLimiter 记录限流器调用次数，验证 benchmark profile 只改变隔离测试配置。
type countingRateLimiter struct {
	calls int
}

// Allow 拒绝请求并记录调用，模拟共享限流器已达到阈值。
func (limiter *countingRateLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	limiter.calls++
	return false, nil
}

// TestBenchmarkModeSkipsBusinessPostRateLimit 验证默认限流保留，而 benchmark profile 只跳过业务 POST 限流。
func TestBenchmarkModeSkipsBusinessPostRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name          string
		benchmarkMode bool
		path          string
		wantStatus    int
		wantCalls     int
	}{
		{name: "default", path: "/api/resources", wantStatus: http.StatusTooManyRequests, wantCalls: 1},
		{name: "benchmark upload", benchmarkMode: true, path: "/api/resources", wantStatus: http.StatusNoContent, wantCalls: 0},
		{name: "benchmark job", benchmarkMode: true, path: "/api/resources/resource-id/jobs", wantStatus: http.StatusNoContent, wantCalls: 0},
		{name: "benchmark other post", benchmarkMode: true, path: "/api/batch/delete", wantStatus: http.StatusTooManyRequests, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			limiter := &countingRateLimiter{}
			handler := &Controller{config: Config{Origin: "http://localhost:5173", RateLimiter: limiter, BenchmarkMode: test.benchmarkMode}}
			engine := gin.New()
			engine.Use(handler.headersAndOrigin)
			engine.POST(test.path, func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })

			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request.Header.Set("Origin", "http://localhost:5173")
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			if response.Code != test.wantStatus || limiter.calls != test.wantCalls {
				t.Fatalf("status=%d calls=%d, want status=%d calls=%d", response.Code, limiter.calls, test.wantStatus, test.wantCalls)
			}
		})
	}
}
