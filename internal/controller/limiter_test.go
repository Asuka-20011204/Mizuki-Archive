package controller

import (
	"net/http/httptest"
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

// TestClientAddressTrustsOnlyControlledProxyHeader 验证直连模式不会接受伪造的 X-Real-IP，受控代理模式才使用合法地址。
func TestClientAddressTrustsOnlyControlledProxyHeader(t *testing.T) {
	direct := httptest.NewRequest("GET", "/", nil)
	direct.RemoteAddr = "10.0.0.8:4321"
	direct.Header.Set("X-Real-IP", "198.51.100.7")
	if address := clientAddress(direct, false); address != "10.0.0.8" {
		t.Fatalf("direct address = %q, want 10.0.0.8", address)
	}
	if address := clientAddress(direct, true); address != "198.51.100.7" {
		t.Fatalf("trusted proxy address = %q, want 198.51.100.7", address)
	}

	invalid := httptest.NewRequest("GET", "/", nil)
	invalid.RemoteAddr = "10.0.0.9:4321"
	invalid.Header.Set("X-Real-IP", "not-an-ip")
	if address := clientAddress(invalid, true); address != "10.0.0.9" {
		t.Fatalf("invalid proxy address = %q, want 10.0.0.9", address)
	}
}
