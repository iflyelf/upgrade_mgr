// Package tlsclient 为 Agent 构造访问控制台的 HTTP 客户端（可选 TLS / 双向 TLS）。
//
// 设计目标：
//   - 默认不改变任何行为（未配置证书时返回 nil，调用方沿用原有客户端）；
//   - 配置 --tls-ca 后校验服务端证书（私有 CA 场景）；
//   - 配置 --tls-cert/--tls-key 后向服务端出示客户端证书（mTLS）。
//
// 注意：TLS 1.2 起步。Windows Server 2008 R2 默认未启用 TLS 1.2 时，
// 需先安装 KB3140245 或改用 HTTP 直连。
package tlsclient

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// Config Agent 侧 TLS 参数（均可留空）。
type Config struct {
	CAFile   string // 校验服务端的 CA 证书（PEM）
	CertFile string // 客户端证书（mTLS）
	KeyFile  string // 客户端私钥（mTLS）
}

// Enabled 是否配置了任意 TLS 参数。
func (c Config) Enabled() bool {
	return c.CAFile != "" || c.CertFile != "" || c.KeyFile != ""
}

// New 构造 HTTP 客户端；未配置任何参数时返回 nil。
//
// 返回的客户端不设置整体 Timeout：长轮询与制品下载依赖请求上下文自身的超时，
// 设置过短的全局超时会导致大文件下载被中断。
func New(cfg Config) (*http.Client, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return nil, fmt.Errorf("tls-cert 与 tls-key 必须同时配置")
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("读取 tls-ca 失败: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls-ca 中没有有效证书")
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("加载客户端证书失败: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:   true,
			TLSClientConfig:     tlsCfg,
			TLSHandshakeTimeout: 15 * time.Second,
		},
	}, nil
}
