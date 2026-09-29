package controller

import (
	"testing"
	"time"
)

// TestRequestLimiterStopsRepeatedRequests 验证 Redis 不可用时本地限流仍能阻止重复高成本请求。
func TestRequestLimiterStopsRepeatedRequests(t *testing.T) {
	limiter := newRequestLimiter()
	for attempt := 0; attempt < 5; attempt++ {
		if !limiter.allowedRequests("register:127.0.0.1", 5, 15*time.Minute) {
			t.Fatalf("request %d should be allowed", attempt+1)
		}
	}
	if limiter.allowedRequests("register:127.0.0.1", 5, 15*time.Minute) {
		t.Fatal("sixth request should be rejected")
	}
	if !limiter.allowedRequests("login:127.0.0.1", 5, 15*time.Minute) {
		t.Fatal("different purpose should use a separate bucket")
	}
}
