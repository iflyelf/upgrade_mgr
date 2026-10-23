// Package netinfo 探测本机网络地址。
//
// 多网卡主机（业务网/管理网/隧道网）仅靠主机名或接口枚举容易选错地址。
// 这里用「向服务端地址发起一次 UDP connect」让内核按路由表选择出口网卡，
// 从而拿到真正用于访问平台的出口 IP；UDP connect 不发送任何报文。
package netinfo

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ExitIP 返回访问 target（host:port 或含协议的 URL）时使用的本机出口 IP。
//
// 探测顺序：
//  1. 向 target 发起 UDP「连接」（不发包），读取内核选定的本地地址；
//  2. 失败时退化为枚举网卡，取第一个非回环、非链路本地的 IPv4；
//  3. 仍失败返回空串（服务端会保留原有出口 IP，不会清空）。
func ExitIP(target string) string {
	if addr := probeUDP(target); addr != "" {
		return addr
	}
	return firstGlobalIPv4()
}

// ExitIPFromServerURL 从 Agent 的服务端地址探测出口 IP。
func ExitIPFromServerURL(serverURL string) string {
	hostport := hostPort(serverURL)
	if hostport == "" {
		return firstGlobalIPv4()
	}
	return ExitIP(hostport)
}

// hostPort 把 URL 或 host[:port] 规范成 host:port（缺省端口按协议补 80/443）。
func hostPort(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		if _, _, err := net.SplitHostPort(raw); err == nil {
			return raw
		}
		return net.JoinHostPort(strings.Trim(raw, "[]"), "80")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// probeUDP 通过 UDP connect 让内核选择出口网卡，成功即返回本地 IP。
//
// net.Dial("udp", ...) 只做路由决策，不产生流量；目标不可达也不影响
// 本地地址的判定（除了 DNS 解析失败）。
func probeUDP(hostport string) string {
	if hostport == "" {
		return ""
	}
	conn, err := net.Dial("udp", hostport)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr == nil || addr.IP == nil || addr.IP.IsUnspecified() || addr.IP.IsLoopback() {
		return ""
	}
	return addr.IP.String()
}

// firstGlobalIPv4 枚举网卡，返回第一个可用的全局 IPv4 地址。
//
// 跳过回环、链路本地（169.254/16）与 docker/veth 等虚拟网卡，
// 避免多网卡环境下上报成容器地址。
func firstGlobalIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(iface.Name)
		virtual := strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") ||
			strings.HasPrefix(name, "cni") || strings.HasPrefix(name, "flannel") ||
			strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "tap")
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			if !virtual {
				return ip4.String()
			}
			if fallback == "" {
				fallback = ip4.String()
			}
		}
	}
	return fallback
}

// NormalizeIP 校验并规范化 IP 地址字符串（仅接受 IPv4/IPv6 字面量）。
func NormalizeIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
		return ip.String()
	}
	return ""
}

var _ = strconv.Itoa // 保留 strconv 引用，便于后续扩展端口解析
