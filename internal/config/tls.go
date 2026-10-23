package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// ServerTLSConfig 根据配置构造控制台的 TLS 配置。
//
// 未配置 tls_cert/tls_key 时返回 nil，调用方继续使用明文 HTTP（历史行为）。
// 配置 tls_client_ca + tls_client_auth=require 即启用双向 TLS：
// 只有持受信客户端证书的 Agent 才能建立连接。
func (c ServerConfig) ServerTLSConfig() (*tls.Config, error) {
	if c.TLSCert == "" && c.TLSKey == "" {
		return nil, nil
	}
	if c.TLSCert == "" || c.TLSKey == "" {
		return nil, fmt.Errorf("tls_cert 与 tls_key 必须同时配置")
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("加载服务端证书失败: %w", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	switch c.TLSClientAuth {
	case "", "none":
		cfg.ClientAuth = tls.NoClientCert
	case "request":
		cfg.ClientAuth = tls.RequestClientCert
	case "require":
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	default:
		return nil, fmt.Errorf("tls_client_auth 只能是 none/request/require")
	}
	if c.TLSClientCA != "" {
		pem, err := os.ReadFile(c.TLSClientCA)
		if err != nil {
			return nil, fmt.Errorf("读取 tls_client_ca 失败: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls_client_ca 中没有有效证书")
		}
		cfg.ClientCAs = pool
	}
	return cfg, nil
}
