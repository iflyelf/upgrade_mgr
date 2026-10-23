// Package tunnelprobe 从平台侧拨测隧道地址，帮助运维在保存前确认地址是否可达。
//
// 平台是 Agent 主动外连服务端，因此这里拨测的是「Agent 将要连接的平台地址」：
// DNS 解析 → TCP 连接 → TLS 握手（https）→ HTTP /health。
// 该测试只能证明「平台所在机器/容器能到达该地址」，不能替代主机侧实测；
// 结果里会给出对应的排查提示。
package tunnelprobe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Stage 单个探测步骤的结果。
type Stage struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
	LatencyMS int64  `json:"latency_ms"`
}

// TLSInfo TLS 证书摘要（https 目标）。
type TLSInfo struct {
	Subject    string   `json:"subject,omitempty"`
	Issuer     string   `json:"issuer,omitempty"`
	NotAfter   string   `json:"not_after,omitempty"`
	DNSNames   []string `json:"dns_names,omitempty"`
	Verified   bool     `json:"verified"`
	VerifyErr  string   `json:"verify_error,omitempty"`
	ServerName string   `json:"server_name,omitempty"`
}

// Result 拨测结果。
type Result struct {
	// Target 是运维填写的原始地址，URL 是补齐协议/端口后的实际拨测地址。
	Target    string   `json:"target"`
	URL       string   `json:"url"`
	OK        bool     `json:"ok"`
	Stages    []Stage  `json:"stages"`
	LatencyMS int64    `json:"latency_ms"`
	TLS       *TLSInfo `json:"tls,omitempty"`
	Service   string   `json:"service,omitempty"`
	Hint      string   `json:"hint,omitempty"`
}

// DefaultTimeout 单次拨测默认超时。
const DefaultTimeout = 5 * time.Second

// Probe 拨测 target。
//
// target 需是已规范化的完整 URL（见 hostnet.ResolveServerURL）；
// timeout<=0 时使用 DefaultTimeout。
func Probe(ctx context.Context, target string, timeout time.Duration) Result {
	return ProbeWithClient(ctx, target, timeout, nil)
}

// ProbeWithClient 使用指定 HTTP 客户端拨测（Agent 侧传自己的 TLS 客户端）。
//
// client 为 nil 时使用内置客户端；即使传入 client，也只使用其 Transport，
// 超时由 ctx 与 timeout 控制。
func ProbeWithClient(ctx context.Context, target string, timeout time.Duration, client *http.Client) Result {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	res := Result{Target: target, Stages: []Stage{}}
	start := time.Now()
	defer func() { res.LatencyMS = time.Since(start).Milliseconds() }()

	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || u.Host == "" {
		res.Hint = "地址格式错误：需要形如 https://10.8.0.5:8090 的绝对地址"
		res.Stages = append(res.Stages, Stage{Name: "解析地址", Detail: errText(err)})
		return res
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		res.Hint = "仅支持 http/https"
		res.Stages = append(res.Stages, Stage{Name: "解析地址", Detail: "不支持的协议 " + u.Scheme})
		return res
	}
	res.URL = strings.TrimRight(u.String(), "/")
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	hostport := net.JoinHostPort(host, port)

	// 1. DNS
	stepStart := time.Now()
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	addrs, lookupErr := net.DefaultResolver.LookupHost(lookupCtx, host)
	cancel()
	if lookupErr != nil {
		// 直接填 IP 时不需要 DNS；只有主机名解析失败才终止。
		if ip := net.ParseIP(host); ip == nil {
			res.Stages = append(res.Stages, Stage{
				Name: "DNS 解析", OK: false, Detail: errText(lookupErr),
				LatencyMS: time.Since(stepStart).Milliseconds(),
			})
			res.Hint = "域名无法解析：确认 DNS/内网解析是否可用，或直接填写 IP"
			return res
		}
		res.Stages = append(res.Stages, Stage{
			Name: "DNS 解析", OK: true, Detail: "目标为 IP，无需解析",
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
	} else {
		res.Stages = append(res.Stages, Stage{
			Name: "DNS 解析", OK: true, Detail: strings.Join(addrs, ", "),
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
	}

	// 2. TCP
	stepStart = time.Now()
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", hostport)
	if err != nil {
		res.Stages = append(res.Stages, Stage{
			Name: "TCP 连接 " + hostport, OK: false, Detail: errText(err),
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
		res.Hint = "端口不通：确认隧道已建立、平台监听地址正确，以及服务器/容器防火墙是否放行该端口"
		return res
	}
	res.Stages = append(res.Stages, Stage{
		Name: "TCP 连接 " + hostport, OK: true, Detail: "已建立连接",
		LatencyMS: time.Since(stepStart).Milliseconds(),
	})

	// 3. TLS（https 目标）
	if u.Scheme == "https" {
		stepStart = time.Now()
		// 沿用调用方客户端的 TLS 配置：Agent 自检时可能配置了私有 CA 或客户端证书，
		// 否则这里会误报证书校验失败。
		tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if client != nil {
			if tr, ok := client.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
				tlsCfg = tr.TLSClientConfig.Clone()
				if tlsCfg.ServerName == "" {
					tlsCfg.ServerName = host
				}
				if tlsCfg.MinVersion == 0 {
					tlsCfg.MinVersion = tls.VersionTLS12
				}
			}
		}
		tlsConn := tls.Client(conn, tlsCfg)
		_ = tlsConn.SetDeadline(time.Now().Add(timeout))
		handshakeErr := tlsConn.HandshakeContext(ctx)
		info := &TLSInfo{ServerName: host}
		if handshakeErr == nil {
			info.Verified = true
			if st := tlsConn.ConnectionState(); len(st.PeerCertificates) > 0 {
				cert := st.PeerCertificates[0]
				info.Subject = cert.Subject.String()
				info.Issuer = cert.Issuer.String()
				info.NotAfter = cert.NotAfter.Format(time.RFC3339)
				info.DNSNames = cert.DNSNames
			}
		} else {
			info.VerifyErr = handshakeErr.Error()
		}
		res.TLS = info
		res.Stages = append(res.Stages, Stage{
			Name: "TLS 握手", OK: handshakeErr == nil, Detail: errText(handshakeErr),
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
		tlsConn.Close()
		if handshakeErr != nil {
			res.Hint = "TLS 握手失败：确认证书域名/IP 是否匹配、Agent 侧是否配置了同一 CA（--tls-ca）"
			return res
		}
	} else {
		conn.Close()
	}

	// 4. HTTP /health
	stepStart = time.Now()
	if client == nil {
		client = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
				ForceAttemptHTTP2: true,
			},
		}
	}
	// 保留 base path（反向代理可能挂在子路径下）。
	healthURL := u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/") + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		res.Stages = append(res.Stages, Stage{Name: "HTTP /health", OK: false, Detail: errText(err)})
		return res
	}
	resp, err := client.Do(req)
	if err != nil {
		res.Stages = append(res.Stages, Stage{
			Name: "HTTP /health", OK: false, Detail: errText(err),
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
		res.Hint = "TCP 可达但 HTTP 失败：确认该地址确实反向代理到 upgrade-mgr（不是别的服务或 TLS 端口）"
		return res
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	service := ""
	if strings.Contains(string(body), "upgrade_mgr") {
		service = "upgrade_mgr"
	}
	ok := resp.StatusCode == http.StatusOK && service != ""
	detail := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if service != "" {
		detail += "，服务标识 " + service
	} else if len(body) > 0 {
		detail += "，响应内容不是 upgrade-mgr"
	}
	res.Stages = append(res.Stages, Stage{
		Name: "HTTP /health", OK: ok, Detail: detail,
		LatencyMS: time.Since(stepStart).Milliseconds(),
	})
	res.Service = service
	res.OK = ok
	if ok {
		res.Hint = "平台侧可达；请再确认目标主机能通过隧道访问该地址（Agent 心跳约 30 秒内生效）"
	} else {
		res.Hint = "地址上运行的可能不是本平台，或反向代理路径/健康检查被拦截"
	}
	return res
}

func errText(err error) string {
	if err == nil {
		return "成功"
	}
	return err.Error()
}
