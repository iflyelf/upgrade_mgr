package handler

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/metrics"
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
	// Local 表示管理员已上传离线文件（优先于上游缓存，平台无需出网）。
	Local     bool       `json:"local"`
	LocalSize int64      `json:"local_size"`
	LocalAt   *time.Time `json:"local_at,omitempty"`
}

// externalBinaryScriptMaxSize 离线脚本上传上限（exe 使用 200MB 全局上限）。
const externalBinaryScriptMaxSize = 10 << 20

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
	// 分页信息：page=0 表示返回全部（未分页）。
	Total int `json:"total"`
	Page  int `json:"page"`
	Size  int `json:"size"`
	Pages int `json:"pages"`
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
	// Merge=true 时把 items 合并进现有白名单（按 name upsert），
	// 并从白名单中删除 Deleted 列出的名称。
	//
	// 表格页服务端分页后只持有当前页条目，必须用合并语义，
	// 否则保存当前页会把其它页的条目清空。
	Merge   bool     `json:"merge"`
	Deleted []string `json:"deleted"`
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
// 支持服务端分页与搜索（?q=&page=&size=）：
// 白名单条目多时避免一次性返回全部（前端也无需全量过滤）。
// 不传 page/size 时返回全部，保持旧行为。
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

		// 搜索：名称或上游地址包含关键字（大小写不敏感）。
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		if q != "" {
			filtered := entries[:0]
			for _, e := range entries {
				if strings.Contains(strings.ToLower(e.Name), q) ||
					strings.Contains(strings.ToLower(e.URL), q) {
					filtered = append(filtered, e)
				}
			}
			entries = filtered
		}

		total := len(entries)
		page := atoiDefault(r.URL.Query().Get("page"), 0)
		size := atoiDefault(r.URL.Query().Get("size"), 0)
		if size > 200 {
			size = 200
		}
		pages := 1
		if page > 0 && size > 0 {
			pages = (total + size - 1) / size
			if pages < 1 {
				pages = 1
			}
			if page > pages {
				page = pages
			}
			start := (page - 1) * size
			end := start + size
			if start > total {
				start = total
			}
			if end > total {
				end = total
			}
			entries = entries[start:end]
		} else {
			page = 0
		}

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
			Total:  total,
			Page:   page,
			Size:   size,
			Pages:  pages,
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

		// 合并语义：以现有白名单为基线，避免分页保存清空其它页。
		raw := make(map[string]string, len(req.Items))
		if req.Merge {
			if existing, err := extbin.Parse(effectiveExternalBinaries(externalBinaryConfig(s))); err == nil {
				for _, e := range existing {
					v := e.URL
					if e.Script {
						v = "script:" + v
					}
					raw[e.Name] = v
				}
			}
		}
		for _, name := range req.Deleted {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			// 删除按名称忽略大小写匹配，避免大小写不同导致删不掉。
			for k := range raw {
				if strings.EqualFold(k, name) {
					delete(raw, k)
				}
			}
		}
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
			// 同一次提交内重名直接拒绝；跨页同名按 upsert 覆盖（merge 语义）。
			if _, dup := raw[name]; dup && !req.Merge {
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

		// 一次性提交：Apply 内部会用「本次提交的 hosts」校验白名单，
		// 因此不会出现「先存白名单被旧 hosts 误拒」，也避免两次写入导致
		// 前一次成功、后一次失败的部分生效（无事务的半更新）。
		kv := map[string]string{
			"server.external_binaries":             extbin.FormatText(raw),
			"server.external_binary_hosts":         strings.Join(hosts, ","),
			"server.external_binary_ttl":           ttl,
			"server.external_binary_allow_private": boolString(req.AllowPrivate),
			"server.external_binary_allow_cidrs":   strings.Join(cidrs, ","),
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

		// 强制刷新时让缓存失效（先删再拉），否则主机可能继续拿到旧文件。
		if req.Force {
			if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
				log.Printf("⚠️ 清除外部文件缓存失败 %s: %v", target, rmErr)
			}
		}
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

// RefreshExternalBinary 强制重新下载并落盘（管理员）。
//
// 与「测试」不同：这里会真正写缓存，用于上游更新后让主机立即拿到新文件。
func RefreshExternalBinary(s *svc.ServiceContext) http.HandlerFunc {
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
		name := strings.TrimSpace(req.Name)
		var entry *extbin.Entry
		for i := range entries {
			if strings.EqualFold(entries[i].Name, name) {
				entry = &entries[i]
				break
			}
		}
		if entry == nil {
			httpx.Fail(w, http.StatusNotFound, "白名单中不存在该名称，请先保存")
			return
		}

		baseDir := externalBinaryBaseDir(cfg)
		target := externalBinaryCachePath(baseDir, *entry)
		// 串行化下载：与普通下载共用同一把文件锁，避免并发写同一文件。
		unlock := lockExternalTarget(target)
		defer unlock()
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		start := time.Now()
		if err := fetchExternalBinary(r.Context(), entry.URL, target, entry.Script,
			effectiveExternalHosts(cfg), externalAllowRule(cfg)); err != nil {
			metrics.ExternalBinaryRequests.WithLabelValues(name, "refresh_error").Inc()
			metrics.ExternalBinaryDuration.WithLabelValues(name, "error").
				Observe(time.Since(start).Seconds())
			auditOp(s, r, auditActionExternalBinaryRefresh, "external_binary", name, entry.URL, err)
			httpx.Fail(w, http.StatusBadGateway, "重新下载失败: "+err.Error())
			return
		}
		metrics.ExternalBinaryRequests.WithLabelValues(name, "refresh").Inc()
		metrics.ExternalBinaryDuration.WithLabelValues(name, "success").
			Observe(time.Since(start).Seconds())
		metrics.ExternalBinaryCacheAge.WithLabelValues(name).Set(0)
		info, _ := os.Stat(target)
		var size int64
		if info != nil {
			size = info.Size()
		}
		auditOp(s, r, auditActionExternalBinaryRefresh, "external_binary", name, entry.URL, nil)
		httpx.OK(w, map[string]any{
			"name": name, "size": size, "elapsed_ms": time.Since(start).Milliseconds(),
		})
	}
}

// RefreshAllExternalBinaries 批量刷新全部缓存（管理员）。
//
// 用途：上游统一升级后一次性更新所有条目。逐个下载，单个失败不影响其它，
// 返回每个条目的结果摘要。
func RefreshAllExternalBinaries(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := externalBinaryConfig(s)
		entries, err := extbin.Parse(effectiveExternalBinaries(cfg))
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "白名单配置错误")
			return
		}
		baseDir := externalBinaryBaseDir(cfg)
		hosts := effectiveExternalHosts(cfg)
		rule := externalAllowRule(cfg)

		type itemResult struct {
			Name      string `json:"name"`
			OK        bool   `json:"ok"`
			Size      int64  `json:"size"`
			ElapsedMS int64  `json:"elapsed_ms"`
			Error     string `json:"error,omitempty"`
		}
		results := make([]itemResult, 0, len(entries))
		okCount, failCount := 0, 0

		for _, e := range entries {
			start := time.Now()
			res := itemResult{Name: e.Name}
			target := externalBinaryCachePath(baseDir, e)
			// 与单项刷新/普通下载共用同一把文件锁，避免并发写同一文件。
			func() {
				unlock := lockExternalTarget(target)
				defer unlock()
				if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
					res.Error = rmErr.Error()
					return
				}
				if fetchErr := fetchExternalBinary(r.Context(), e.URL, target, e.Script, hosts, rule); fetchErr != nil {
					res.Error = fetchErr.Error()
					return
				}
				if info, statErr := os.Stat(target); statErr == nil {
					res.Size = info.Size()
				}
				res.OK = true
			}()
			res.ElapsedMS = time.Since(start).Milliseconds()
			if res.OK {
				okCount++
				metrics.ExternalBinaryRequests.WithLabelValues(e.Name, "refresh").Inc()
				metrics.ExternalBinaryCacheAge.WithLabelValues(e.Name).Set(0)
			} else {
				failCount++
				metrics.ExternalBinaryRequests.WithLabelValues(e.Name, "refresh_error").Inc()
			}
			metrics.ExternalBinaryDuration.WithLabelValues(e.Name, boolResult(res.OK)).
				Observe(time.Since(start).Seconds())
			results = append(results, res)
		}

		auditOpDetails(s, r, auditActionExternalBinaryRefresh, "external_binary", "all", "",
			boolErr(failCount == 0, fmt.Sprintf("%d 个条目刷新失败", failCount)),
			map[string]any{"ok": okCount, "failed": failCount}, nil)
		httpx.OK(w, map[string]any{
			"total": len(entries), "ok": okCount, "failed": failCount, "items": results,
		})
	}
}

// boolResult 把成功/失败映射为指标 label。
func boolResult(ok bool) string {
	if ok {
		return "success"
	}
	return "error"
}

// probeExternalBinary 拉取文件头部做可用性探测。
// 注意：必须用命名返回值，否则 defer 里修改 res 不会写回返回值
// （`return res` 在 defer 执行前已完成值拷贝，耗时永远为 0）。
func probeExternalBinary(ctx context.Context, entry extbin.Entry, hosts []string,
	rule extbin.AllowRule) (res externalBinaryTestResult) {
	res = externalBinaryTestResult{Name: entry.Name, URL: entry.URL}
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
	if li, lerr := os.Stat(externalBinaryLocalPath(baseDir, e)); lerr == nil && !li.IsDir() {
		at := li.ModTime()
		item.Local = true
		item.LocalSize = li.Size()
		item.LocalAt = &at
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

// externalBinaryLocalPath 管理员上传的离线文件路径（优先于上游缓存）。
func externalBinaryLocalPath(baseDir string, e extbin.Entry) string {
	name := strings.ToLower(e.Name)
	if !e.Script {
		name += ".exe"
	}
	return filepath.Join(baseDir, ".external-local", name)
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
	auditActionExternalBinarySave    = "external_binary.save"
	auditActionExternalBinaryTest    = "external_binary.test"
	auditActionExternalBinaryRefresh = "external_binary.refresh"
)

var _ = sql.ErrNoRows
var _ = safepath.EnsureWithin

// UploadExternalBinary 上传离线文件到平台（管理员）。
//
// 典型场景：控制台/目标机都无法出网（如 Windows Server 2008 R2 无 TLS 1.2），
// 由管理员在本机下载 exe/脚本后上传；/api/external/binary/{name} 将优先返回该本地文件。
func UploadExternalBinary(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
		name = strings.TrimSuffix(name, ".exe")
		cfg := externalBinaryConfig(s)
		entries, err := extbin.Parse(effectiveExternalBinaries(cfg))
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "白名单配置错误")
			return
		}
		var entry *extbin.Entry
		for i := range entries {
			if strings.EqualFold(entries[i].Name, name) {
				entry = &entries[i]
				break
			}
		}
		if entry == nil {
			httpx.Fail(w, http.StatusNotFound, "白名单中不存在该名称，请先保存")
			return
		}

		baseDir := externalBinaryBaseDir(cfg)
		target := externalBinaryLocalPath(baseDir, *entry)
		if err := safepath.EnsureWithin(baseDir, target); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "非法文件名")
			return
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}

		limit := int64(externalBinaryMaxSize)
		if entry.Script {
			limit = externalBinaryScriptMaxSize
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
		// multipart 超过内存阈值的部分会落到临时文件，必须显式清理。
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "request body too large") {
				status = http.StatusRequestEntityTooLarge
			}
			httpx.Fail(w, status, "解析上传内容失败: "+err.Error())
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, "缺少文件字段 file")
			return
		}
		defer file.Close()

		tmp, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)

		n, copyErr := io.Copy(tmp, io.LimitReader(file, limit+1))
		closeErr := tmp.Close()
		if copyErr != nil {
			httpx.FailErr(w, http.StatusBadRequest, copyErr)
			return
		}
		if closeErr != nil {
			httpx.FailErr(w, http.StatusInternalServerError, closeErr)
			return
		}
		if n == 0 {
			httpx.Fail(w, http.StatusBadRequest, "上传文件为空")
			return
		}
		if n > limit {
			httpx.Fail(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("文件超过上限 %d 字节", limit))
			return
		}
		if err := validateExternalContent(tmpPath, entry.Script); err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := os.Chmod(tmpPath, 0644); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		// Windows 上 Rename 不能覆盖已存在文件；先删除旧文件。
		_ = os.Remove(target)
		if err := os.Rename(tmpPath, target); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		auditOp(s, r, "external_binary.upload", "external_binary", entry.Name, entry.Name, nil)
		httpx.OK(w, buildExternalBinaryItem(baseDir, *entry))
	}
}

// DeleteExternalBinaryUpload 删除管理员上传的离线文件（恢复使用上游/缓存）。
func DeleteExternalBinaryUpload(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
		name = strings.TrimSuffix(name, ".exe")
		cfg := externalBinaryConfig(s)
		entries, err := extbin.Parse(effectiveExternalBinaries(cfg))
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "白名单配置错误")
			return
		}
		var entry *extbin.Entry
		for i := range entries {
			if strings.EqualFold(entries[i].Name, name) {
				entry = &entries[i]
				break
			}
		}
		if entry == nil {
			httpx.Fail(w, http.StatusNotFound, "白名单中不存在该名称")
			return
		}
		baseDir := externalBinaryBaseDir(cfg)
		target := externalBinaryLocalPath(baseDir, *entry)
		if err := safepath.EnsureWithin(baseDir, target); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "非法文件名")
			return
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		auditOp(s, r, "external_binary.upload_delete", "external_binary", entry.Name, entry.Name, nil)
		httpx.OK(w, map[string]any{"deleted": true})
	}
}
