package main

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// TestIsLocalOriginRequiresExactHostname 验证 benchmark 只能接受明确的本机主机名。
func TestIsLocalOriginRequiresExactHostname(t *testing.T) {
	for _, test := range []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "localhost", origin: "http://localhost:18080", want: true},
		{name: "ipv4", origin: "http://127.0.0.1:18080", want: true},
		{name: "ipv6", origin: "http://[::1]:18080", want: true},
		{name: "lookalike", origin: "http://localhost.example:18080", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := url.Parse(test.origin)
			if err != nil {
				t.Fatalf("parse origin: %v", err)
			}
			if got := isLocalOrigin(parsed); got != test.want {
				t.Fatalf("isLocalOrigin(%q) = %v, want %v", test.origin, got, test.want)
			}
		})
	}
}

// TestLoadEnvironmentLoadsFileWithoutOverwritingProcessEnvironment 验证文件值可读且不会覆盖进程已有配置。
func TestLoadEnvironmentLoadsFileWithoutOverwritingProcessEnvironment(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("MIZUKI_TEST_FROM_FILE=from-file\nMIZUKI_TEST_PRECEDENCE=from-file\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	setTestEnvironment(t, "MIZUKI_TEST_FROM_FILE", "")
	setTestEnvironment(t, "MIZUKI_TEST_PRECEDENCE", "from-process")
	t.Setenv("MIZUKI_ENV_FILE", envFile)

	if err := loadEnvironment(); err != nil {
		t.Fatalf("load environment: %v", err)
	}
	if got := os.Getenv("MIZUKI_TEST_FROM_FILE"); got != "from-file" {
		t.Fatalf("loaded value = %q, want %q", got, "from-file")
	}
	if got := os.Getenv("MIZUKI_TEST_PRECEDENCE"); got != "from-process" {
		t.Fatalf("precedence value = %q, want %q", got, "from-process")
	}
}

// TestLoadEnvironmentAllowsMissingDefaultFile 验证未创建本地配置时仍可依赖系统环境变量启动。
func TestLoadEnvironmentAllowsMissingDefaultFile(t *testing.T) {
	setTestEnvironment(t, "MIZUKI_ENV_FILE", "")
	workingDirectory := t.TempDir()
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	// 清理回调恢复工作目录，避免污染后续测试。
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	if err := loadEnvironment(); err != nil {
		t.Fatalf("missing default env file returned error: %v", err)
	}
}

// setTestEnvironment 暂设或清除单个环境变量，并在用例结束时恢复原值。
func setTestEnvironment(t *testing.T, name, value string) {
	previous, existed := os.LookupEnv(name)
	if value == "" {
		_ = os.Unsetenv(name)
	} else {
		_ = os.Setenv(name, value)
	}
	// 清理回调区分原来不存在与原来为空的环境变量。
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(name, previous)
			return
		}
		_ = os.Unsetenv(name)
	})
}
