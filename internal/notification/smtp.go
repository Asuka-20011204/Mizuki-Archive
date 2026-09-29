// Package notification 封装外部通知通道，不在业务层暴露 SMTP 协议和凭据。
package notification

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

var ErrTLSRequired = errors.New("SMTP server does not support required TLS")

// SMTP 保存发信所需的连接配置；密码仅驻留在进程内，不写入日志或邮件内容。
type SMTP struct {
	host      string
	port      int
	username  string
	password  string
	from      string
	tlsConfig *tls.Config
}

// NewSMTP 在启动阶段检查配置，避免运行时才发现发件人或凭据不完整。
func NewSMTP(host string, port int, username, password, from string) (*SMTP, error) {
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "\r\n ") || port < 1 || port > 65535 || username == "" || password == "" || !validMailbox(from) {
		return nil, errors.New("invalid SMTP configuration")
	}
	return &SMTP{host: host, port: port, username: username, password: password, from: from}, nil
}

// NewSMTPWithRootCA 创建使用额外根证书池的 SMTP 发送器；默认系统证书链仍由 NewSMTP 负责。
func NewSMTPWithRootCA(host string, port int, username, password, from string, rootCAPEM []byte) (*SMTP, error) {
	sender, err := NewSMTP(host, port, username, password, from)
	if err != nil {
		return nil, err
	}
	if len(rootCAPEM) == 0 {
		return nil, errors.New("SMTP root CA is empty")
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(rootCAPEM) {
		return nil, errors.New("invalid SMTP root CA")
	}
	sender.tlsConfig = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: pool}
	return sender, nil
}

// validMailbox 只接受单个裸邮箱地址，阻断显示名和 CRLF 注入邮件头。
func validMailbox(value string) bool {
	if value == "" || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Name == "" && address.Address == value
}

// SendCode 只通过已验证的 TLS 连接发送验证码；发送失败由调用方决定是否重试。
func (sender *SMTP) SendCode(ctx context.Context, recipient, code string) error {
	if !validMailbox(recipient) || len(code) != 6 {
		return errors.New("invalid verification message")
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return errors.New("invalid verification message")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	address := net.JoinHostPort(sender.host, fmt.Sprint(sender.port))
	connection, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return errors.New("connect to SMTP server failed")
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return errors.New("set SMTP timeout failed")
	}
	rawConnection := connection
	stopCancel := context.AfterFunc(ctx, func() { rawConnection.Close() })
	defer stopCancel()
	tlsConfig := &tls.Config{ServerName: sender.host, MinVersion: tls.VersionTLS12}
	if sender.tlsConfig != nil {
		tlsConfig = sender.tlsConfig
	}
	if sender.port == 465 {
		// 465 端口从连接开始就使用 TLS；其他端口必须先确认 STARTTLS，不能降级发送密码。
		secure := tls.Client(connection, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return errors.New("SMTP TLS handshake failed")
		}
		connection = secure
	}
	client, err := smtp.NewClient(connection, sender.host)
	if err != nil {
		return errors.New("SMTP greeting failed")
	}
	defer client.Close()
	if sender.port != 465 {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return ErrTLSRequired
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return errors.New("SMTP STARTTLS failed")
		}
	}
	if err := client.Auth(smtp.PlainAuth("", sender.username, sender.password, sender.host)); err != nil {
		return errors.New("SMTP authentication failed")
	}
	if err := client.Mail(sender.from); err != nil {
		return errors.New("SMTP sender rejected")
	}
	if err := client.Rcpt(recipient); err != nil {
		return errors.New("SMTP recipient rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP message rejected")
	}
	body := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("您的 Mizuki Archive 验证码：%s\r\n请勿向他人透露。\r\n", code)))
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", sender.from, recipient, mime.QEncoding.Encode("utf-8", "Mizuki Archive 验证码"))
	for len(body) > 76 {
		message += body[:76] + "\r\n"
		body = body[76:]
	}
	message += body + "\r\n"
	if _, err := writer.Write([]byte(message)); err != nil {
		writer.Close()
		return errors.New("SMTP message write failed")
	}
	if err := writer.Close(); err != nil {
		return errors.New("SMTP message delivery failed")
	}
	// SMTP 的最终 250 已确认收信；此后即使请求取消也不能谎报失败，避免上层重复发送。
	return nil
}
