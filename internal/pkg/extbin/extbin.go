// Package extbin 管理「控制台代缓存外部文件」的白名单。
//
// 背景：老系统（Windows Server 2008 R2 自带 PowerShell 2.0 / .NET 2.0）不支持
// TLS 1.2，无法直连 GitHub。控制台（Go）支持 TLS 1.2，可代下载后通过 HTTP 提供。
//
// 安全边界（缺一不可）：
//   - 名称白名单：只允许配置里声明的 name，接口不接受任意 URL；
//   - 上游域名白名单：只允许 github.com / raw.githubusercontent.com / 代理域名；
//   - 拒绝解析到环回/私网地址的主机，防止 SSRF 访问内网与云元数据；
//   - 大小上限与内容校验（exe 必须 MZ，脚本不能是 HTML）。
package extbin

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// 前缀：标明该条目是文本脚本而不是可执行文件。
const scriptPrefix = "script:"

// DefaultHosts 默认允许的上游域名。
var DefaultHosts = []string{
	"github.com",
	"raw.githubusercontent.com",
	"proxy.xiaonuo.live",
}

// DefaultTTLSeconds 默认缓存有效期。
const DefaultTTLSeconds = 24 * 60 * 60

// Entry 一条白名单。
type Entry struct {
	Name string
	URL  string
	// Script 为 true 表示文本脚本（不做 MZ 校验）。
	Script bool
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidName 校验名称（会被拼进 URL 路径与缓存文件名）。
func ValidName(name string) bool {
	return name != "" && len(name) <= 128 && nameRe.MatchString(name)
}

// Parse 解析白名单配置。
//
// 支持两种书写：map 形式（配置文件）与 "name=url" 行/逗号列表（设置页与 env）。
// 值前缀 "script:" 表示文本脚本。
func Parse(raw map[string]string) ([]Entry, error) {
	entries := make([]Entry, 0, len(raw))
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		e, err := parseOne(name, raw[name])
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// ParseText 解析 "name=url" 形式的文本（换行或逗号分隔）。
func ParseText(text string) (map[string]string, error) {
	out := map[string]string{}
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || strings.HasPrefix(f, "#") {
			continue
		}
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("白名单项 %q 缺少 =", f)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ValidName(k) {
			return nil, fmt.Errorf("白名单名称 %q 非法（仅允许字母数字、点、下划线、短横线）", k)
		}
		if v == "" {
			return nil, fmt.Errorf("白名单 %s 缺少地址", k)
		}
		out[k] = v
	}
	return out, nil
}

// FormatText 把白名单格式化为设置页可编辑的文本。
func FormatText(raw map[string]string) string {
	if len(raw) == 0 {
		return ""
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(name + "=" + raw[name])
	}
	return b.String()
}

func parseOne(name, raw string) (Entry, error) {
	name = strings.TrimSpace(name)
	raw = strings.TrimSpace(raw)
	if !ValidName(name) {
		return Entry{}, fmt.Errorf("白名单名称 %q 非法（仅允许字母数字、点、下划线、短横线）", name)
	}
	script := false
	if strings.HasPrefix(strings.ToLower(raw), scriptPrefix) {
		script = true
		raw = strings.TrimSpace(raw[len(scriptPrefix):])
	}
	if err := ValidateURL(raw); err != nil {
		return Entry{}, fmt.Errorf("白名单 %s 地址非法: %w", name, err)
	}
	return Entry{Name: name, URL: raw, Script: script}, nil
}

// ValidateURL 校验上游地址是绝对 http/https 且不含凭据/片段。
func ValidateURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("地址为空")
	}
	if len(raw) > 2048 {
		return fmt.Errorf("地址过长（最多 2048 字符）")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("格式错误: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("仅支持 http/https")
	}
	if u.Host == "" {
		return fmt.Errorf("缺少主机名")
	}
	if u.User != nil {
		return fmt.Errorf("不允许包含凭据")
	}
	if u.Fragment != "" {
		return fmt.Errorf("不允许包含片段")
	}
	return nil
}

// HostAllowed 判断上游主机是否在白名单内；hosts 为空时用默认白名单。
func HostAllowed(raw string, hosts []string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("地址格式错误")
	}
	host := strings.ToLower(u.Hostname())
	if len(hosts) == 0 {
		hosts = DefaultHosts
	}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		// 允许配置带端口（如 proxy.example.com:8443），比较时忽略端口。
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		if host == h {
			return nil
		}
	}
	return fmt.Errorf("主机 %s 不在 external_binary_hosts 白名单内", host)
}

// AllowRule 内网放行规则。
//
// 背景：很多企业的镜像站/制品库在私网（如 http://nexus.corp:8081、10.8.0.5），
// 一刀切拒绝内网会把正常用法挡掉。因此提供显式开关与 CIDR 白名单：
//   - AllowPrivate=true：完全放开内网/环回（最宽松，仅建议内网部署使用）；
//   - AllowCIDRs 非空：只放行列出的网段（推荐，最小权限）；
//   - 两者都未配置：保持原有防护，拒绝一切内网地址。
type AllowRule struct {
	AllowPrivate bool
	AllowCIDRs   []string
}

// permits 判断 target IP 是否被显式放行。
func (r AllowRule) permits(ip net.IP) bool {
	// 精确放行优先：放行网段内的一律允许（即使 AllowPrivate 未开）。
	for _, cidr := range r.AllowCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		if _, ipnet, err := net.ParseCIDR(cidr); err == nil && ipnet.Contains(ip) {
			return true
		}
		if single := net.ParseIP(cidr); single != nil && single.Equal(ip) {
			return true
		}
	}
	if r.AllowPrivate {
		return true
	}
	return false
}

// ValidateCIDRs 校验 CIDR/IP 列表格式。
func ValidateCIDRs(list []string) error {
	for _, cidr := range list {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		if net.ParseIP(cidr) != nil {
			continue
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("网段 %q 格式错误（示例 10.0.0.0/8 或 10.8.0.5）", cidr)
		}
	}
	return nil
}

// CheckHost 完整校验上游地址：域名白名单 + 内网地址策略。
//
// 页面「测试下载」与实际下载共用本函数，保证测试通过即真实可下载。
func CheckHost(raw string, hosts []string, rule AllowRule) error {
	if err := HostAllowed(raw, hosts); err != nil {
		return err
	}
	return checkInternal(raw, rule)
}

// EnsureNotInternal 拒绝解析到环回/私网/链路本地地址的主机（默认严格模式）。
func EnsureNotInternal(raw string) error {
	return checkInternal(raw, AllowRule{})
}

// checkInternal 按放行规则检查目标地址。
//
// 主机名解析失败时放行（由实际下载失败兜底），避免 DNS 抖动导致误拒。
func checkInternal(raw string, rule AllowRule) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("地址格式错误")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return rule.checkIP(ip)
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	for _, ip := range addrs {
		if err := rule.checkIP(ip); err != nil {
			return err
		}
	}
	return nil
}

// checkIP 判定单个 IP：非内网直接放行；内网需被 AllowRule 显式放行。
func (r AllowRule) checkIP(ip net.IP) error {
	internal := ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
	if !internal {
		return nil
	}
	if r.permits(ip) {
		return nil
	}
	// 提示如何放行，避免运维反复试错。
	hint := "如需访问内网镜像，请勾选「允许内网地址」或把网段加入「放行网段」"
	if ip.IsLoopback() {
		hint = "回环地址仅用于本机调试；确需访问请勾选「允许内网地址」"
	}
	return fmt.Errorf("拒绝访问内网/环回地址 %s（%s）", ip, hint)
}

// ValidateConfig 校验整套白名单（配置文件与设置页保存时使用）。
//
// 注意：这里只校验域名白名单，不解析 DNS（保存时不应依赖网络）。
// 内网地址策略在保存时只校验网段格式，实际解析在下载/测试时进行。
func ValidateConfig(raw map[string]string, hosts []string) error {
	if _, err := Parse(raw); err != nil {
		return err
	}
	for name, v := range raw {
		pure := strings.TrimSpace(v)
		if strings.HasPrefix(strings.ToLower(pure), scriptPrefix) {
			pure = strings.TrimSpace(pure[len(scriptPrefix):])
		}
		if err := HostAllowed(pure, hosts); err != nil {
			return fmt.Errorf("白名单 %s: %w", name, err)
		}
	}
	return nil
}

// ValidateConfigWithRule 校验白名单 + 内网放行规则（页面保存时使用）。
func ValidateConfigWithRule(raw map[string]string, hosts []string, rule AllowRule) error {
	if err := ValidateCIDRs(rule.AllowCIDRs); err != nil {
		return err
	}
	return ValidateConfig(raw, hosts)
}
