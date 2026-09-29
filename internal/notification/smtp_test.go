package notification

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// TestNewSMTPRejectsInvalidConfig 防止缺少凭据或非法发件地址被带入运行阶段。
func TestNewSMTPRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		port     int
		username string
		password string
		from     string
	}{
		{"missing host", "", 587, "user", "secret", "user@example.com"},
		{"invalid port", "mail.example.com", 0, "user", "secret", "user@example.com"},
		{"missing password", "mail.example.com", 587, "user", "", "user@example.com"},
		{"header injection", "mail.example.com", 587, "user", "secret", "user@example.com\r\nBcc: attacker@example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSMTP(test.host, test.port, test.username, test.password, test.from); err == nil {
				t.Fatal("expected invalid configuration")
			}
		})
	}
}

// TestNewSMTPWithRootCARejectsInvalidCertificate 防止自定义 SMTP 根证书配置被静默接受为无效信任链。
func TestNewSMTPWithRootCARejectsInvalidCertificate(t *testing.T) {
	if _, err := NewSMTPWithRootCA("mail.example.com", 587, "user", "secret", "user@example.com", []byte("not-a-certificate")); err == nil {
		t.Fatal("expected invalid root CA error")
	}
}

// TestSMTPSendCodeRejectsUnsafeInput 验证收件地址与验证码不会进入未校验的邮件头。
func TestSMTPSendCodeRejectsUnsafeInput(t *testing.T) {
	sender, err := NewSMTP("mail.example.com", 587, "user", "secret", "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, recipient := range []string{"", "a@example.com\r\nBcc: attacker@example.com", "Name <a@example.com>"} {
		if err := sender.SendCode(context.Background(), recipient, "123456"); err == nil {
			t.Fatalf("expected invalid recipient %q", recipient)
		}
	}
	if err := sender.SendCode(context.Background(), "person@example.com", "123\r\nInjected"); err == nil {
		t.Fatal("expected invalid code")
	}
}

// TestSMTPSendCodeRequiresTLS 确认不支持 STARTTLS 的服务器绝不会收到认证或邮件正文。
func TestSMTPSendCodeRequiresTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 2)
	go func() {
		defer close(commands)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		connection.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(connection)
		fmt.Fprint(connection, "220 mail.example.com ESMTP\r\n")
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			return
		}
		commands <- line
		fmt.Fprint(connection, "250 mail.example.com\r\n")
		line, readErr = reader.ReadString('\n')
		if readErr == nil {
			commands <- line
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	sender, err := NewSMTP("127.0.0.1", address.Port, "user", "secret", "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.SendCode(context.Background(), "person@example.com", "123456"); !errors.Is(err, ErrTLSRequired) {
		t.Fatalf("expected TLS required, got %v", err)
	}
	if command := <-commands; !strings.HasPrefix(command, "EHLO ") {
		t.Fatalf("expected EHLO, got %q", command)
	}
	for command := range commands {
		if strings.Contains(command, "AUTH") || strings.Contains(command, "MAIL") {
			t.Fatalf("sensitive command sent without TLS: %q", command)
		}
	}
}

// TestSMTPSendCodeOverSTARTTLS 使用本地可信证书验证完整握手和邮件交付，不连接真实邮箱。
func TestSMTPSendCodeOverSTARTTLS(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(certificateDER)
	if err != nil {
		t.Fatal(err)
	}
	trust := x509.NewCertPool()
	trust.AddCert(certificate)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	delivered := make(chan string, 1)
	failures := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			failures <- acceptErr
			return
		}
		defer connection.Close()
		connection.SetDeadline(time.Now().Add(5 * time.Second))
		message, serveErr := receiveTestMessage(connection, tls.Certificate{Certificate: [][]byte{certificateDER}, PrivateKey: privateKey})
		if serveErr != nil {
			failures <- serveErr
			return
		}
		delivered <- message
	}()
	sender, err := NewSMTP("127.0.0.1", listener.Addr().(*net.TCPAddr).Port, "user", "secret", "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	sender.tlsConfig = &tls.Config{ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12, RootCAs: trust}
	if err := sender.SendCode(context.Background(), "person@example.com", "123456"); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-delivered:
		parts := strings.SplitN(message, "\r\n\r\n", 2)
		if len(parts) != 2 || !strings.Contains(parts[0], "To: person@example.com") || !strings.Contains(parts[0], "Content-Transfer-Encoding: base64") {
			t.Fatalf("incorrect email headers: %q", message)
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(strings.ReplaceAll(parts[1], "\r\n", ""))
		if decodeErr != nil || !strings.Contains(string(decoded), "123456") {
			t.Fatalf("incorrect email content: %q", message)
		}
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP test server did not finish")
	}
}

// receiveTestMessage 模拟要求 STARTTLS 的 SMTP 服务，并返回经加密连接接收的邮件正文。
func receiveTestMessage(connection net.Conn, certificate tls.Certificate) (string, error) {
	reader := bufio.NewReader(connection)
	if _, err := fmt.Fprint(connection, "220 local ESMTP\r\n"); err != nil {
		return "", err
	}
	if _, err := reader.ReadString('\n'); err != nil {
		return "", err
	}
	if _, err := fmt.Fprint(connection, "250-local\r\n250 STARTTLS\r\n"); err != nil {
		return "", err
	}
	if command, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(command, "STARTTLS") {
		return "", fmt.Errorf("STARTTLS expected: %q, %v", command, err)
	}
	if _, err := fmt.Fprint(connection, "220 Ready to start TLS\r\n"); err != nil {
		return "", err
	}
	secure := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err := secure.Handshake(); err != nil {
		return "", err
	}
	reader = bufio.NewReader(secure)
	if _, err := reader.ReadString('\n'); err != nil {
		return "", err
	}
	if _, err := fmt.Fprint(secure, "250-local\r\n250 AUTH PLAIN\r\n"); err != nil {
		return "", err
	}
	for _, response := range []string{"235 Authenticated\r\n", "250 Sender accepted\r\n", "250 Recipient accepted\r\n", "354 End data with dot\r\n"} {
		if _, err := reader.ReadString('\n'); err != nil {
			return "", err
		}
		if _, err := fmt.Fprint(secure, response); err != nil {
			return "", err
		}
	}
	var message strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == ".\r\n" {
			break
		}
		message.WriteString(line)
	}
	if _, err := fmt.Fprint(secure, "250 Queued\r\n"); err != nil {
		return "", err
	}
	return message.String(), nil
}
