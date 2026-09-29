package main

import "testing"

// TestProcessingWorkerCount 验证并发配额默认值及越界配置会被启动检查拒绝。
func TestProcessingWorkerCount(t *testing.T) {
	for _, testcase := range []struct {
		input string
		want  int
		valid bool
	}{
		{"", 2, true}, {"1", 1, true}, {"4", 4, true}, {"0", 0, false}, {"5", 0, false}, {"abc", 0, false},
	} {
		workers, err := processingWorkerCount(testcase.input)
		if workers != testcase.want || (err == nil) != testcase.valid {
			t.Fatalf("配置 %q: workers=%d err=%v", testcase.input, workers, err)
		}
	}
}
