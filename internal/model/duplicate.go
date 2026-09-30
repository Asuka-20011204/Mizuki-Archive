package model

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// DuplicateHint 只返回同账号可见资料的名称与 ID，不返回原件内容或内部文件路径。
type DuplicateHint struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ExternalLinkKey 将 HTTP(S) 位置规范化后生成摘要；仅比较文本，绝不访问输入地址。
func ExternalLinkKey(location string) string {
	parsed, err := url.Parse(strings.TrimSpace(location))
	if err != nil || parsed.User != nil || parsed.Opaque != "" || parsed.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return ""
	}
	port := parsed.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return ""
		}
	}
	if port == "80" && scheme == "http" || port == "443" && scheme == "https" {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return ""
	}
	path := parsed.Path
	if path == "" {
		path = "/"
	}
	canonical := (&url.URL{Scheme: scheme, Host: host, Path: path, RawPath: parsed.RawPath, RawQuery: query.Encode()}).String()
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}
