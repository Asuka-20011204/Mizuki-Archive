package main

import "testing"

// TestPercentile 验证常用分位点边界和线性插值结果。
func TestPercentile(t *testing.T) {
	values := []float64{10, 20, 30, 40}
	tests := []struct {
		name     string
		fraction float64
		want     float64
	}{
		{name: "p0", fraction: 0, want: 10},
		{name: "p50", fraction: 0.5, want: 25},
		{name: "p99", fraction: 0.99, want: 39.7},
		{name: "p100", fraction: 1, want: 40},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := percentile(values, test.fraction); got-test.want > 1e-9 || test.want-got > 1e-9 {
				t.Fatalf("percentile(%v, %v) = %v, want %v", values, test.fraction, got, test.want)
			}
		})
	}
}

// TestPercentileEmpty 验证空样本集返回稳定的零值。
func TestPercentileEmpty(t *testing.T) {
	if got := percentile(nil, 0.95); got != 0 {
		t.Fatalf("percentile(nil, 0.95) = %v, want 0", got)
	}
}

// TestNormalizeBaseURL 验证只接受 origin URL，并拒绝路径、查询参数和缺少主机的地址。
func TestNormalizeBaseURL(t *testing.T) {
	valid, err := normalizeBaseURL("http://localhost:18082/")
	if err != nil {
		t.Fatalf("normalizeBaseURL returned error: %v", err)
	}
	if valid != "http://localhost:18082" {
		t.Fatalf("normalizeBaseURL returned %q", valid)
	}
	for _, raw := range []string{"localhost:18082", "http://localhost:18082/api", "http://localhost:18082?debug=1"} {
		if _, err := normalizeBaseURL(raw); err == nil {
			t.Fatalf("normalizeBaseURL(%q) accepted invalid URL", raw)
		}
	}
}

// TestSummarizeHTTPTotal 验证总吞吐以及 5xx、429、超时分类不会依赖调用方再计算。
func TestSummarizeHTTPTotal(t *testing.T) {
	metrics := []httpMetric{
		{Status: 200},
		{Status: 503},
		{Status: 429, Expected429: true},
		{Status: 0, Timeout: true},
		{Status: 429},
	}
	got := summarizeHTTPTotal(metrics, 2)
	if got["count"] != 5 || got["requests_per_second"] != 2.5 || got["five_xx"] != 1 || got["expected_429"] != 1 || got["unexpected_429"] != 1 || got["timeouts"] != 1 {
		t.Fatalf("unexpected HTTP total: %#v", got)
	}
}
