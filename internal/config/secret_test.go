package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadSecretUsesFileValue 验证 Secret 文件可以提供配置，并且只去除文件末尾换行。
func TestReadSecretUsesFileValue(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretPath, []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	t.Setenv("MIZUKI_TEST_SECRET", "")
	t.Setenv("MIZUKI_TEST_SECRET_FILE", secretPath)

	value, err := ReadSecret("MIZUKI_TEST_SECRET")
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if value != "from-file" {
		t.Fatalf("secret value = %q, want %q", value, "from-file")
	}
}

// TestReadSecretRejectsAmbiguousSources 验证非空环境变量和 Secret 文件同时存在时会被拒绝。
func TestReadSecretRejectsAmbiguousSources(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretPath, []byte("from-file"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	t.Setenv("MIZUKI_TEST_SECRET", "from-environment")
	t.Setenv("MIZUKI_TEST_SECRET_FILE", secretPath)

	_, err := ReadSecret("MIZUKI_TEST_SECRET")
	if err == nil || !strings.Contains(err.Error(), "must not both contain values") {
		t.Fatalf("ambiguous secret error = %v", err)
	}
}

// TestReadSecretReturnsEnvironmentValue 验证未配置文件时保持原有环境变量读取行为。
func TestReadSecretReturnsEnvironmentValue(t *testing.T) {
	t.Setenv("MIZUKI_TEST_SECRET", "from-environment")
	t.Setenv("MIZUKI_TEST_SECRET_FILE", "")

	value, err := ReadSecret("MIZUKI_TEST_SECRET")
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if value != "from-environment" {
		t.Fatalf("secret value = %q, want %q", value, "from-environment")
	}
}
