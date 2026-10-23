package handler

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/safepath"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// externalBinaryItem 外部文件条目（表格页使用）。
type externalBinaryItem struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Script 为 true 表示文本脚本（不做 MZ 校验）。
	Script bool `json:"script"`
	// PlatformURL 是 Agent/脚本可直接访问的平台地址。
	PlatformURL string `json:"platform_url"`
	// Cached 表示控制台已有可用缓存。
	Cached     bool       `json:"cached"`
	CachedSize int64      `json:"cached_size"`
	CachedAt   *time.Time `json:"cached_at,omitempty"`
}

// externalBinaryListResponse 列表响应。
type externalBinaryListResponse struct {
	Items  []externalBinaryItem `json:"items"`
	Hosts  []string             `json:"hosts"`
	TTL    string               `json:"ttl"`
	Source string               `json:"source"` // config / default
	Dir    string               `json:"dir"`    // 缓存目录（便于排查）
	// 内网放行策略：内网镜像站需要显式开启。
	AllowPrivate bool     `json:"allow_private"`
	AllowCIDRs   []string `json:"allow_cidrs"`
}

// externalBinarySaveRequest 保存请求。
type externalBinarySaveRequest struct {
	Items []struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Script bool   `json:"script"`
	} `json:"items"`
	Hosts        []string `json:"hosts"`
	TTL          string   `json:"ttl"`
	AllowPrivate bool     `json:"allow_private"`
	AllowCIDRs   []string `json:"allow_cidrs"`
}

// externalBinaryTestRequest 测试下载请求。
type externalBinaryTestRequest struct {
	Name string `json:"name"`
	// URL 可选：测试尚未保存的候选地址（仍需通过域名白名单与内网校验）。
	URL   string `json:"url"`
	Force bool   `json:"force"` // 忽略缓存，强制重新拉取
}

// externalBinaryTestResult 测试下载结果。
type externalBinaryTestResult struct {
	Name         string     `json:"name"`
	URL          string     `json:"url"`
	OK           bool       `json:"ok"`
	Cached       bool       `json:"cached"`
	StatusCode   int        `json:"status_code"`
	ContentType  string     `json:"content_type"`
	Size         int64      `json:"size"`
	ElapsedMS    int64      `json:"elapsed_ms"`
	CheckedBytes int        `json:"checked_bytes"`
	CachedAt     *time.Time `json:"cached_at,omitempty"`
	Error        string     `json:"error,omitempty"`
	Hint         string     `json:"hint,omitempty"`
}

// ListExternalBinaries 返回外部文件白名单（管理员）。
//
// 同时给出设置为空的回退来源（default）与缓存状态，页面据此渲染表格。
func ListExternalBinaries(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := externalBinaryConfig(s)
		raw := effectiveExternalBinaries(cfg)
		entries, err := extbin.Parse(raw)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, fmt.Errorf("白名单配置错误: %w", err))
			return
		}
		baseDir := externalBinaryBaseDir(cfg)
		items := make([]externalBinaryItem, 0, len(entries))
		for _, e := range entries {
			items = append(items, buildExternalBinaryItem(baseDir, e))
		}
		ttl := ""
		if cfg != nil {
			ttl = cfg.Server.ExternalBinaryTTL
		}
		source := "config"
		if cfg == nil || len(cfg.Server.ExternalBinaries) == 0 {
			source = "default"
		}
		resp := externalBinaryListResponse{
			Items:  items,
			Hosts:  effectiveExternalHosts(cfg),
			TTL:    ttl,
			Source: source,
			Dir:    baseDir,
		}
		if cfg != nil {
			resp.AllowPrivate = cfg.Server.ExternalBinaryAllowPrivate
			resp.AllowCIDRs = cfg.Server.ExternalBinaryAllowCIDRs
		}
		httpx.OK(w, resp)
	}
}

// SaveExternalBinaries 保存外部文件白名单（超级管理员，立即生效）。
//
// 写入 app_settings（与设置页同一份数据），因此保存后接口立即读到新值，
// 无需重启：热加载由 Handler 每次读取设置快照实现。
func SaveExternalBinaries(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Settings == nil {
			httpx.Fail(w, http.StatusInternalServerError, "设置服务不可用")
			return
		}
		var req externalBinarySaveRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}

		// 组装为 name -> "script:url" 的映射，复用统一校验。
		raw := make(map[string]string, len(req.Items))
		for _, it := range req.Items {
			name := strings.TrimSpace(it.Name)
			url := strings.TrimSpace(it.URL)
			if name == "" || url == "" {
				httpx.Fail(w, http.StatusBadRequest, "名称与地址不能为空")
				return
			}
			value := url
			if it.Script {
				value = "script:" + url
			}
			if _, dup := raw[name]; dup {
				httpx.Fail(w, http.StatusBadRequest, "名称重复: "+name)
				return
			}
			raw[name] = value
		}
		if _, err := extbin.Parse(raw); err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		hosts := normalizeHostList(req.Hosts)
		cidrs := normalizeHostList(req.AllowCIDRs)
		rule := extbin.AllowRule{AllowPrivate: req.AllowPrivate, AllowCIDRs: cidrs}
		if err := extbin.ValidateConfigWithRule(raw, hosts, rule); err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		ttl := strings.TrimSpace(req.TTL)
		if ttl != "" {
			d, err := time.ParseDuration(ttl)
			if err != nil || d <= 0 {
				httpx.Fail(w, http.StatusBadRequest, "缓存有效期格式错误（示例 24h）")
				return
			}
		}

		// 先更新域名/内网策略，再保存白名单：setting.Apply 的 Validate 使用
		// 当前 Config 中的 hosts 做校验，若先存白名单会被旧 hosts 误拒。
		if _, err := s.Settings.Apply(r.Context(), map[string]string{
			"server.external_binary_hosts":         strings.Join(hosts, ","),
			"server.external_binary_allow_private": boolString(req.AllowPrivate),
			"server.external_binary_allow_cidrs":   strings.Join(cidrs, ","),
			"server.external_binary_ttl":           ttl,
		}); err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}

		kv := map[string]string{
			"server.external_binaries":     extbin.FormatText(raw),
			"server.external_binary_hosts": strings.Join(hosts, ","),
			"server.external_binary_ttl":   ttl,
		}
		applied, err := s.Settings.Apply(r.Context(), kv)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		auditOp(s, r, auditActionExternalBinarySave, "external_binary", "",
			fmt.Sprintf("count=%d applied=%s", len(raw), strings.Join(applied, ",")), nil)
		httpx.OK(w, map[string]any{"saved": len(raw), "applied": applied})
	}
}

// TestExternalBinary 立即拉取一次外部文件，返回大小/HTTP 状态（超级管理员）。
//
// 只读取前 64KB 用于校验，不落盘、不进缓存：既能快速确认地址可用，
// 又不会因为“测试”把大文件下载到磁盘。
func TestExternalBinary(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req externalBinaryTestRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		cfg := externalBinaryConfig(s)
		entries, err := extbin.Parse(effectiveExternalBinaries(cfg))
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "白名单配置错误")
			return
		}

		res := externalBinaryTestResult{Name: strings.TrimSpace(req.Name)}
		var entry *extbin.Entry
		for i := range entries {
			if strings.EqualFold(entries[i].Name, res.Name) {
				entry = &entries[i]
				break
			}
		}
		if entry == nil {
			// 允许测试尚未保存的候选地址（name 可空）。
			if strings.TrimSpace(req.URL) == "" {
				httpx.Fail(w, http.StatusBadRequest, "请提供已保存的名称，或直接填写待测试地址")
				return
			}
			if err := extbin.ValidateURL(req.URL); err != nil {
				httpx.Fail(w, http.StatusBadRequest, err.Error())
				return
			}
			script := strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.URL)), "script:")
			url := strings.TrimSpace(req.URL)
			if script {
				url = strings.TrimSpace(url[len("script:"):])
			}
			entry = &extbin.Entry{Name: res.Name, URL: url, Script: script}
			if entry.Name == "" {
				entry.Name = "candidate"
			}
			res.Name = entry.Name
		}
		res.URL = entry.URL

		// 允许缓存命中直接返回（页面可勾选强制刷新）。
		baseDir := externalBinaryBaseDir(cfg)
		target := externalBinaryCachePath(baseDir, *entry)
		if !req.Force && freshFile(target, effectiveExternalTTL(cfg)) {
			if info, statErr := os.Stat(target); statErr == nil {
				at := info.ModTime()
				res.OK = true
				res.Cached = true
				res.Size = info.Size()
				res.ContentType = "缓存文件"
				res.CheckedBytes = 0
				res.ElapsedMS = 0
				res.Hint = "命中控制台缓存（勾选「强制重新拉取」可验证上游地址）"
				res.CachedAt = &at
				httpx.OK(w, res)
				return
			}
		}

		probeCtx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		probe := probeExternalBinary(probeCtx, *entry, effectiveExternalHosts(cfg), externalAllowRule(cfg))
		probe.Name = res.Name
		probe.URL = entry.URL
		auditOpDetails(s, r, auditActionExternalBinaryTest, "external_binary", res.Name, entry.URL,
			boolErr(probe.OK, probe.Error), map[string]any{
				"status_code": probe.StatusCode, "size": probe.Size,
			}, nil)
		httpx.OK(w, probe)
	}
}

// probeExternalBinary 拉取文件头部做可用性探测。
func probeExternalBinary(ctx context.Context, entry extbin.Entry, hosts []string,
	rule extbin.AllowRule) externalBinaryTestResult {
	res := externalBinaryTestResult{Name: entry.Name, URL: entry.URL}
	start := time.Now()
	defer func() { res.ElapsedMS = time.Since(start).Milliseconds() }()

	// 与真实下载共用同一校验：测试通过即代表可下载。
	if err := extbin.CheckHost(entry.URL, hosts, rule); err != nil {
		res.Error = err.Error()
		if strings.Contains(err.Error(), "内网") || strings.Contains(err.Error(), "环回") {
			res.Hint = "地址解析到内网：勾选「允许内网地址」或把网段加入「放行网段」"
		} else {
			res.Hint = "该域名不在「允许的上游域名」内，请先加入白名单"
		}
		return res
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	// 只取头部，避免测试大文件时耗时过长。
	req.Header.Set("Range", "bytes=0-65535")
	req.Header.Set("User-Agent", "upgrade-mgr/external-binary-test")
	resp, err := externalBinaryClient.Do(req)
	if err != nil {
		res.Error = err.Error()
		res.Hint = "网络不可达：确认控制台能访问该地址（DNS/代理/防火墙）"
		return res
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode
	res.ContentType = resp.Header.Get("Content-Type")
	res.Size = resp.ContentLength
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		res.Error = fmt.Sprintf("源站返回 HTTP %d", resp.StatusCode)
		res.Hint = "上游地址不可用，或该资源不存在（404）/需要鉴权（401/403）"
		return res
	}

	head, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	res.CheckedBytes = len(head)
	if len(head) == 0 {
		res.Error = "响应内容为空"
		return res
	}
	sample := strings.TrimSpace(strings.ToLower(string(head)))
	if entry.Script {
		for _, prefix := range []string{"<!doctype", "<html", "<head", "<body", "<?xml"} {
			if strings.HasPrefix(sample, prefix) {
				res.Error = "下载内容不是脚本，而是 HTML/XML 页面（可能是错误页或需要登录）"
				res.Hint = "检查上游地址是否指向原始文件（raw.githubusercontent.com），而不是网页"
				return res
			}
		}
	} else if !strings.HasPrefix(sample, "mz") {
		res.Error = "下载内容不是 Windows 可执行文件（缺少 MZ 头）"
		res.Hint = "确认地址指向 .exe 文件本身（GitHub Release 的下载链接）"
		return res
	}
	res.OK = true
	if res.Size <= 0 {
		res.Size = int64(len(head))
		res.Hint = "源站未返回 Content-Length，仅校验了前 64KB"
	}
	return res
}

// buildExternalBinaryItem 组装表格行（含缓存状态）。
func buildExternalBinaryItem(baseDir string, e extbin.Entry) externalBinaryItem {
	item := externalBinaryItem{
		Name:        e.Name,
		URL:         e.URL,
		Script:      e.Script,
		PlatformURL: "/api/external/binary/" + e.Name,
	}
	info, err := os.Stat(externalBinaryCachePath(baseDir, e))
	if err == nil && !info.IsDir() {
		at := info.ModTime()
		item.Cached = true
		item.CachedSize = info.Size()
		item.CachedAt = &at
	}
	return item
}

// externalBinaryBaseDir 返回缓存根目录（与 Agent 下载目录一致）。
func externalBinaryBaseDir(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.Server.AgentBinaryDir) != "" {
		return cfg.Server.AgentBinaryDir
	}
	return "./dist"
}

// externalBinaryCachePath 计算缓存文件路径。
func externalBinaryCachePath(baseDir string, e extbin.Entry) string {
	name := strings.ToLower(e.Name)
	if !e.Script {
		name += ".exe"
	}
	return filepath.Join(baseDir, ".external-cache", name)
}

// normalizeHostList 去空、去重、转小写。
func normalizeHostList(hosts []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// boolString 把布尔转成设置项需要的字符串。
func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// boolErr 把失败结果转为 error（供审计记录）。
func boolErr(ok bool, msg string) error {
	if ok {
		return nil
	}
	if msg == "" {
		msg = "外部文件探测未通过"
	}
	return fmt.Errorf("%s", msg)
}

// 审计动作（与 handler 层的命名保持一致）。
const (
	auditActionExternalBinarySave = "external_binary.save"
	auditActionExternalBinaryTest = "external_binary.test"
)

var _ = sql.ErrNoRows
var _ = safepath.EnsureWithin
