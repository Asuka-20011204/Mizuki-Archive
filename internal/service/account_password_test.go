package service

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestHashAccountPassword 验证注册输入边界以及数据库只保存可验证的密码哈希。
func TestHashAccountPassword(t *testing.T) {
	for _, input := range []struct{ username, password string }{
		{"2numbers", "correct horse battery staple"},
		{"bad@name", "correct horse battery staple"},
		{" validname", "correct horse battery staple"},
		{"validname", "short password"},
		{"validname", strings.Repeat("a", 73)},
	} {
		if _, _, err := HashAccountPassword(input.username, input.password); !errors.Is(err, ErrAccountInput) {
			t.Errorf("invalid input accepted: %q, error=%v", input.username, err)
		}
	}
	username, hash, err := HashAccountPassword("valid_user", "correct horse battery staple")
	if err != nil || username != "valid_user" || bytes.Contains(hash, []byte("correct horse battery staple")) {
		t.Fatalf("password hashing failed: username=%q err=%v", username, err)
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte("correct horse battery staple")); err != nil {
		t.Fatalf("saved password hash is not verifiable: %v", err)
	}
}
