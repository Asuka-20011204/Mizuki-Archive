// Package config 提供启动配置的安全读取辅助函数。
package config

import (
	"fmt"
	"os"
	"strings"
)

// ReadSecret 读取环境变量或同名的 Docker Secrets 文件，并避免把秘密内容写入错误信息。
// 当 NAME_FILE 指向文件时，文件内容优先于空的 NAME 环境变量；如果两者都提供了非空值则拒绝启动，避免配置来源不明确。
func ReadSecret(name string) (string, error) {
	inlineValue, hasInlineValue := os.LookupEnv(name)
	filePath, hasFilePath := os.LookupEnv(name + "_FILE")
	if hasFilePath && strings.TrimSpace(filePath) != "" {
		if hasInlineValue && strings.TrimSpace(inlineValue) != "" {
			return "", fmt.Errorf("%s and %s_FILE must not both contain values", name, name)
		}
		contents, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read %s_FILE failed", name)
		}
		return strings.TrimRight(string(contents), "\r\n"), nil
	}
	return inlineValue, nil
}
