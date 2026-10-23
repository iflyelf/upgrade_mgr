// Package hostnet 处理主机网络地址：多网卡出口 IP 与隧道地址。
//
// 平台是 Agent 主动外连服务端（服务端不反连主机），因此「隧道地址」指的是
// Agent 应该连接的平台地址：
//   - 部分主机只能通过隧道网段访问平台；
//   - 隧道可能只提供 IP 或 IP:端口，需要沿用安装时的协议与端口。
//
// 该包只做字符串规范化与校验，不做任何网络探测。
package hostnet

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// maxTunnelLen 隧道地址长度上限（与 hosts.tunnel_ip 列宽一致）。
const maxTunnelLen = 512

// NormalizeAddr 校验并规范化运维填写的隧道地址。
//
// 接受三种写法：
//   - 纯 IP：        10.8.0.5
//   - IP:端口：      10.8.0.5:8090
//   - 完整 URL：     https://tunnel.example.com:8443
//
// 返回规范化后的字符串；空字符串表示「未配置隧道」（合法）。
func NormalizeAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > maxTunnelLen {
		return "", fmt.Errorf("隧道地址过长（最多 %d 字符）", maxTunnelLen)
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("隧道地址包含控制字符")
		}
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("隧道地址格式错误: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", fmt.Errorf("隧道地址仅支持 http/https")
		}
		if u.Hostname() == "" {
			return "", fmt.Errorf("隧道地址缺少主机名")
		}
		if u.User != nil {
			return "", fmt.Errorf("隧道地址不能包含凭据")
		}
		u.Path = strings.TrimRight(u.Path, "/")
		return strings.TrimRight(u.String(), "/"), nil
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		// 没有端口：必须是合法 IP 或主机名。
		host = strings.Trim(raw, "[]")
		port = ""
	}
	if host == "" {
		return "", fmt.Errorf("隧道地址缺少主机名")
	}
	if net.ParseIP(host) == nil && !validHostname(host) {
		return "", fmt.Errorf("隧道地址不是合法的 IP 或主机名: %s", host)
	}
	if port != "" {
		if p, err := net.LookupPort("tcp", port); err != nil || p <= 0 || p > 65535 {
			return "", fmt.Errorf("隧道地址端口非法: %s", port)
		}
	}
	return raw, nil
}

// ResolveServerURL 把隧道地址解析成下发给 Agent 的完整 http(s) URL。
//
// defaultURL 是 Agent 安装时的默认地址，用于补齐协议与端口：
//   - 隧道为完整 URL：原样使用；
//   - 隧道为 IP:端口：沿用默认协议；
//   - 隧道为纯 IP：沿用默认协议与端口（默认端口为空时按协议补 80/443）。
//
// 隧道为空或非法时返回空串（Agent 据此回退默认地址）。
func ResolveServerURL(tunnel, defaultURL string) string {
	tunnel = strings.TrimSpace(tunnel)
	if tunnel == "" {
		return ""
	}
	if strings.Contains(tunnel, "://") {
		u, err := url.Parse(tunnel)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return ""
		}
		u.Path, u.RawQuery, u.Fragment = strings.TrimRight(u.Path, "/"), "", ""
		return strings.TrimRight(u.String(), "/")
	}

	base, err := url.Parse(strings.TrimSpace(defaultURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		base = &url.URL{Scheme: "http"}
	}
	host := tunnel
	if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
		host = strings.Trim(host, "[]")
		port := base.Port()
		if port == "" {
			// 隧道只给 IP 时必须显式补端口，否则下发的 URL 会落到 80/443 默认端口，
			// 而平台的默认地址很可能监听在 8090。
			if base.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		host = net.JoinHostPort(host, port)
	}
	if host == "" {
		return ""
	}
	base.Host = host
	base.Path, base.RawQuery, base.Fragment = "", "", ""
	return strings.TrimRight(base.String(), "/")
}

// NormalizeExitIP 校验 Agent 上报的出口 IP（只接受 IP 字面量）。
func NormalizeExitIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
		return ip.String()
	}
	return ""
}

// validHostname 粗略校验主机名（字母数字、点、连字符）。
func validHostname(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i, r := range label {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
			if !ok {
				return false
			}
			if r == '-' && (i == 0 || i == len(label)-1) {
				return false
			}
		}
	}
	return true
}
