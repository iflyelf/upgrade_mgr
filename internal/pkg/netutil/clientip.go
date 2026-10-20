// Package netutil 提供客户端 IP 解析，全项目唯一实现（避免多处实现发散）。
//
// 安全要点：X-Forwarded-For / X-Real-IP 由客户端可写，服务直接暴露时完全不可信。
// 仅当部署在受信反向代理之后（显式开启 trustProxy）才解析这些头部，
// 否则一律以 TCP 连接来源 RemoteAddr 为准。
//
// 开启 trustProxy 后，XFF 按「从右往左」解析：最右侧条目由离服务最近的代理追加，
// 最可信；最左侧条目完全由客户端控制，可被伪造。因此：
//   - 配置了受信代理列表：从右往左跳过受信代理，取第一个非受信 IP；
//   - 未配置受信代理列表：取最右侧的合法 IP（即直连代理看到的对端）。
package netutil

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ProxyList 受信代理网段集合；nil 或空表示未配置。
type ProxyList struct {
	nets []*net.IPNet
}

// ParseTrustedProxies 解析 CIDR 或单个 IP 列表。非法条目被跳过并汇总到 error 返回，
// 合法条目仍然生效（调用方可选择仅告警）。
func ParseTrustedProxies(list []string) (*ProxyList, error) {
	p := &ProxyList{}
	var bad []string
	for _, raw := range list {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			ip := net.ParseIP(s)
			if ip == nil {
				bad = append(bad, s)
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				s += "/32"
			} else {
				s += "/128"
			}
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			bad = append(bad, raw)
			continue
		}
		p.nets = append(p.nets, n)
	}
	if len(bad) > 0 {
		return p, fmt.Errorf("忽略非法受信代理条目: %s", strings.Join(bad, ", "))
	}
	return p, nil
}

// Empty 是否未配置任何受信代理。
func (p *ProxyList) Empty() bool { return p == nil || len(p.nets) == 0 }

// Contains 判断 ip 是否属于受信代理。
func (p *ProxyList) Contains(ip net.IP) bool {
	if p == nil || ip == nil {
		return false
	}
	for _, n := range p.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP 返回客户端 IP（不区分受信代理列表的兼容入口）。
//
// trustProxy=false（默认）：只取 RemoteAddr，忽略任何转发头，防止伪造。
// trustProxy=true：取 X-Forwarded-For 最右侧合法 IP，其次 X-Real-IP，最后 RemoteAddr。
func ClientIP(r *http.Request, trustProxy bool) string {
	return ClientIPWithProxies(r, trustProxy, nil)
}

// ClientIPWithProxies 返回客户端 IP，trusted 为受信代理列表（可为 nil）。
func ClientIPWithProxies(r *http.Request, trustProxy bool, trusted *ProxyList) string {
	remote := remoteHost(r.RemoteAddr)
	if !trustProxy {
		return remote
	}
	// 配置了受信列表时，直连对端本身不受信则不解析任何转发头（防止直连伪造）。
	if !trusted.Empty() && !trusted.Contains(net.ParseIP(remote)) {
		return remote
	}

	entries := xffEntries(r)
	if len(entries) > 0 {
		if trusted.Empty() {
			if ip := net.ParseIP(entries[len(entries)-1]); ip != nil {
				return ip.String()
			}
			return remote
		}
		var leftmostTrusted string
		for i := len(entries) - 1; i >= 0; i-- {
			ip := net.ParseIP(entries[i])
			if ip == nil {
				// 链路中出现非法条目：其左侧内容不可信，退回最近一个已确认的地址
				break
			}
			if !trusted.Contains(ip) {
				return ip.String()
			}
			leftmostTrusted = ip.String()
		}
		if leftmostTrusted != "" {
			return leftmostTrusted
		}
		return remote
	}

	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return ip.String()
		}
	}
	return remote
}

// xffEntries 合并所有 X-Forwarded-For 头（多个同名头按出现顺序拼接）。
func xffEntries(r *http.Request) []string {
	var out []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(v, ",") {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func remoteHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
