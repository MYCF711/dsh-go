package transport

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTestCert 生成一份自签证书（证书 + 私钥 PEM），供 TLS 监听测试用。
func writeTestCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成私钥失败: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签发证书失败: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("写证书失败: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatalf("写私钥失败: %v", err)
	}
	return certPath, keyPath
}

// TestWebserverTLSServesHTTPS 钉住官方 `webserver.tls`（默认 null = 明文）：
// 给了证书与私钥就必须真的起 HTTPS，URL 也必须是 https://。
func TestWebserverTLSServesHTTPS(t *testing.T) {
	certPath, keyPath := writeTestCert(t)
	t.Setenv(EnvWebserverTLSCert, certPath)
	t.Setenv(EnvWebserverTLSKey, keyPath)

	s, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	base, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	if !strings.HasPrefix(base, "https://") {
		t.Fatalf("开了 TLS 的服务器应当返回 https://，实际 %q", base)
	}

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		// /connect 成功是 303 + Set-Cookie；不跟跳，才能判到这条 TLS 响应本身。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(base + "/connect?token=" + s.Token())
	if err != nil {
		t.Fatalf("HTTPS 请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("/connect over TLS 状态码 = %d，期望 303", resp.StatusCode)
	}
	if resp.TLS == nil {
		t.Fatal("这不是一条真 TLS 连接（resp.TLS 为空）")
	}
}

// TestWebserverTLSDefaultsToPlainHTTPAndFailsLoudOnHalfConfig 钉住两件事：
// 两个 env 都空时保持明文（等价官方 null）；只给一个必须 fail-loud，
// 不能静默退回明文（那会让"以为开了 HTTPS"的部署裸奔）。
func TestWebserverTLSDefaultsToPlainHTTPAndFailsLoudOnHalfConfig(t *testing.T) {
	certPath, _ := writeTestCert(t)

	t.Setenv(EnvWebserverTLSCert, "")
	t.Setenv(EnvWebserverTLSKey, "")
	s, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	base, err := s.Start()
	if err != nil {
		t.Fatalf("默认（无证书）应当明文监听: %v", err)
	}
	if !strings.HasPrefix(base, "http://") {
		t.Fatalf("默认应当是 http://，实际 %q", base)
	}
	s.Close()

	t.Setenv(EnvWebserverTLSCert, certPath)
	t.Setenv(EnvWebserverTLSKey, "")
	s2, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := s2.Start(); err == nil {
		s2.Close()
		t.Fatal("只给证书不给私钥时必须 fail-loud，实际 Start 成功了")
	}
}
