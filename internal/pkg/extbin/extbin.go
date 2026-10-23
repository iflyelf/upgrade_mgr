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

// EnsureNotInternal 拒绝解析到环回/私网/链路本地地址的主机。
//
// 这是 SSRF 的第二道防线：即使白名单被改成内网域名，也不能访问内网。
// 主机名解析失败时放行（由实际下载失败兜底），避免 DNS 抖动导致误拒。
func EnsureNotInternal(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("地址格式错误")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return checkIP(ip)
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	for _, ip := range addrs {
		if err := checkIP(ip); err != nil {
			return err
		}
	}
	return nil
}

func checkIP(ip net.IP) error {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("拒绝访问内网/环回地址 %s", ip)
	}
	return nil
}

// ValidateConfig 校验整套白名单（配置文件与设置页保存时使用）。
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
