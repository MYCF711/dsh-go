package transport

import (
	"crypto/tls"
	"fmt"
	"os"
	"strings"
)

// TLS 监听：官方 `webserver.tls`（默认 null = 明文 HTTP）。
//
// 宿主原来只有明文：`http.Server` 只设了 Handler 与 ReadHeaderTimeout，没有
// 证书入口。官方把这个参数暴露成可切的 HTTPS，所以这里用两个环境变量表达
// "证书 + 私钥"：**都空 = 明文**（等价官方 null），**都非空 = HTTPS**。
//
// 只设一个必须 fail-loud：静默退回明文会让"以为开了 HTTPS"的部署裸奔在网络上，
// 这是少数值得让 Start 直接失败的情形。
const (
	// EnvWebserverTLSCert 是证书文件（PEM）路径；与 EnvWebserverTLSKey 必须成对出现。
	EnvWebserverTLSCert = "DSHD_HTTP_TLS_CERT"
	// EnvWebserverTLSKey 是私钥文件（PEM）路径。
	EnvWebserverTLSKey = "DSHD_HTTP_TLS_KEY"
)

// webserverTLSConfig 按官方 webserver.tls 的形状装配 TLS。
//
// 返回 (nil, nil) 表示"不启 TLS"（两个 env 都没设）——调用方据此保持明文监听，
// 不要把它当成错误。返回错误表示配置不完整或证书加载失败。
func webserverTLSConfig() (*tls.Config, error) {
	certFile := strings.TrimSpace(os.Getenv(EnvWebserverTLSCert))
	keyFile := strings.TrimSpace(os.Getenv(EnvWebserverTLSKey))
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("transport: TLS 需要 %s 与 %s 成对设置（当前 cert=%q key=%q）",
			EnvWebserverTLSCert, EnvWebserverTLSKey, certFile, keyFile)
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("transport: 加载 TLS 证书/私钥失败: %w", err)
	}
	// TLS 1.2 起步：本机回环上也不会用 1.0/1.1，留着只是给降级攻击开口子。
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
