package repository

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestValidatePasswordHashRejectsMalformedInput 验证迁移入口不会接受空值或非 bcrypt 格式。
func TestValidatePasswordHashRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name string
		hash []byte
	}{
		{name: "empty", hash: nil},
		{name: "plain text", hash: []byte("not-a-bcrypt-hash")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidatePasswordHash(test.hash); err == nil {
				t.Fatalf("ValidatePasswordHash(%q) returned nil", test.name)
			}
		})
	}
}

// TestValidatePasswordHashAcceptsBcrypt 验证正常生成的 bcrypt 哈希可以进入迁移流程。
func TestValidatePasswordHashAcceptsBcrypt(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("test password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate bcrypt hash: %v", err)
	}
	if err := ValidatePasswordHash(hash); err != nil {
		t.Fatalf("ValidatePasswordHash returned error: %v", err)
	}
}
