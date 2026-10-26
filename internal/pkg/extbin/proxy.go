package extbin

import (
	"net"
	"net/url"
	"strings"
)

// DefaultProxyBase 外部文件代缓存的默认加速代理。
//
// 代理服务接受 ?url=<原始 URL>，国内访问 GitHub/raw 时由它代为拉取。
// 设置页可修改或清空（清空表示直连）。
const DefaultProxyBase = "https://proxy.xiaonuo.live/proxy?url="

var defaultProxyHosts = []string{
	"github.com",
	"raw.githubusercontent.com",
	"release-assets.githubusercontent.com",
	"github-releases.githubusercontent.com",
	"objects.githubusercontent.com",
}

// DefaultProxyHosts 返回默认走加速代理的上游域名。
func DefaultProxyHosts() []string {
	return append([]string(nil), defaultProxyHosts...)
}

// ProxyURL 判断 source 是否应走代理，并返回代理 URL。
//
// 规则：
//   - proxyBase 为空：直连；
//   - source 不是 http/https：直连；
//   - source 主机不在 proxyHosts：直连（例如内网镜像）；
//   - source 已经是代理服务地址：直连，避免二次代理；
//   - 否则返回 proxyBase + url.QueryEscape(source)。
//
// proxyBase 支持两种写法：
//   - 以 url= 结尾（默认）：直接追加编码后的源地址；
//   - 含 {url} 占位符：替换为编码后的源地址；
//   - 其他：自动追加 ?url= 或 &url=。
func ProxyURL(source, proxyBase string, proxyHosts []string) (string, bool) {
	source = strings.TrimSpace(source)
	proxyBase = strings.TrimSpace(proxyBase)
	if source == "" || proxyBase == "" {
		return source, false
	}
	su, err := url.Parse(source)
	if err != nil || su.Hostname() == "" ||
		(su.Scheme != "http" && su.Scheme != "https") {
		return source, false
	}
	bu, err := url.Parse(proxyBase)
	if err != nil || bu.Hostname() == "" {
		return source, false
	}
	// 已经指向代理本身：不要再套一层。
	if strings.EqualFold(su.Host, bu.Host) {
		return source, false
	}
	host := strings.ToLower(su.Hostname())
	matched := false
	for _, h := range proxyHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		if host == h {
			matched = true
			break
		}
	}
	if !matched {
		return source, false
	}

	escaped := url.QueryEscape(source)
	if strings.Contains(proxyBase, "{url}") {
		return strings.ReplaceAll(proxyBase, "{url}", escaped), true
	}
	if strings.Contains(proxyBase, "url=") {
		return proxyBase + escaped, true
	}
	sep := "?"
	if strings.Contains(proxyBase, "?") {
		sep = "&"
	}
	if strings.HasSuffix(proxyBase, "?") || strings.HasSuffix(proxyBase, "&") {
		sep = ""
	}
	return proxyBase + sep + "url=" + escaped, true
}
