package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/metrics"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/keyedlock"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/safepath"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// externalBinaryMaxSize 单个文件上限，防止异常响应把磁盘写满。
const externalBinaryMaxSize = 200 << 20

// utf8BOM UTF-8 BOM：PowerShell 2.0（Server 2008 R2）据此按 UTF-8 解码 .ps1。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// externalBinaryClient 下载外部文件使用的客户端。
var externalBinaryClient = &http.Client{Timeout: 10 * time.Minute}

// defaultExternalBinaries 首次部署的默认白名单。
//
// 运行期以 server.external_binaries 配置（配置文件或设置页）为准，修改后立即生效，
// 无需重启：Handler 每次请求都从 s.Settings 快照读取。
func defaultExternalBinaries() map[string]string {
	return extbin.DefaultBinaries()
}

// effectiveExternalBinaries 返回当前生效的白名单。
//
// 优先使用配置（配置文件或设置页）；为空时用内置默认值，
// 保证升级后原有 Windows 接入不因缺少配置而中断。
func effectiveExternalBinaries(cfg *config.Config) map[string]string {
	if cfg != nil && len(cfg.Server.ExternalBinaries) > 0 {
		return cfg.Server.ExternalBinaries
	}
	return defaultExternalBinaries()
}

// effectiveExternalHosts 返回允许的上游域名。
func effectiveExternalHosts(cfg *config.Config) []string {
	if cfg != nil && len(cfg.Server.ExternalBinaryHosts) > 0 {
		return cfg.Server.ExternalBinaryHosts
	}
	return extbin.DefaultHosts
}

// effectiveExternalProxy 返回加速代理地址；空表示直连。
func effectiveExternalProxy(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Server.ExternalBinaryProxy)
}

// effectiveExternalProxyHosts 返回走加速代理的上游域名；空表示不启用代理。
func effectiveExternalProxyHosts(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	return cfg.Server.ExternalBinaryProxyHosts
}

// hostsWithProxyHost 把代理主机加入本次请求的域名白名单。
//
// 代理地址由管理员配置，属于受信出口；源站域名仍先用原白名单校验，
// 代理请求本身不应因为漏配 proxy 域名而失败。
func hostsWithProxyHost(hosts []string, proxyBase string) []string {
	u, err := neturl.Parse(proxyBase)
	if err != nil || u.Hostname() == "" {
		return hosts
	}
	out := append([]string(nil), hosts...)
	out = append(out, u.Hostname())
	return out
}

// externalAllowRule 返回内网放行策略。
//
// 默认严格（拒绝内网）；内网镜像站通过 allow_private 或 allow_cidrs 显式放行。
func externalAllowRule(cfg *config.Config) extbin.AllowRule {
	rule := extbin.AllowRule{}
	if cfg == nil {
		return rule
	}
	rule.AllowPrivate = cfg.Server.ExternalBinaryAllowPrivate
	rule.AllowCIDRs = cfg.Server.ExternalBinaryAllowCIDRs
	return rule
}

// effectiveExternalTTL 返回缓存有效期。
func effectiveExternalTTL(cfg *config.Config) time.Duration {
	ttl := time.Duration(extbin.DefaultTTLSeconds) * time.Second
	if cfg != nil && strings.TrimSpace(cfg.Server.ExternalBinaryTTL) != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(cfg.Server.ExternalBinaryTTL)); err == nil && d > 0 {
			ttl = d
		}
	}
	return ttl
}

// externalBinaryConfig 从 ServiceContext 取当前配置。
func externalBinaryConfig(s *svc.ServiceContext) *config.Config {
	if s == nil {
		return nil
	}
	if s.Settings != nil {
		snap := s.Settings.Snapshot()
		return &snap
	}
	return s.Config
}

// ExternalBinaryDownload 下载白名单外部文件（无鉴权，内容为公开文件）。
//
// 白名单来自 server.external_binaries（配置文件 / 设置页），支持热加载：
// 每次请求都读取最新快照，保存设置后立即生效，无需重启。
// 缓存落盘到 <agent_binary_dir>/.external-cache。
//
// 查询参数 local=1：只返回管理员上传的离线文件或已有缓存，**不触发上游下载**。
// 供老系统（PowerShell 2.0/CLR 2.0）快速探测平台是否已备好文件，避免平台现场
// 下载 GitHub 导致脚本超时。
func ExternalBinaryDownload(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
		// 允许带扩展名访问：/api/external/binary/foo.exe 等同 foo
		name = strings.TrimSuffix(name, ".exe")
		if !extbin.ValidName(name) {
			http.NotFound(w, r)
			return
		}

		cfg := externalBinaryConfig(s)
		entries, err := extbin.Parse(effectiveExternalBinaries(cfg))
		if err != nil {
			log.Printf("⚠️ external_binaries 配置非法: %v", err)
			httpx.Fail(w, http.StatusInternalServerError, "外部文件白名单配置错误，请检查设置")
			return
		}
		// 名称按大小写不敏感匹配（URL 已转小写，配置里可能保留大写）。
		var asset *extbin.Entry
		for i := range entries {
			if strings.EqualFold(entries[i].Name, name) {
				asset = &entries[i]
				break
			}
		}
		if asset == nil {
			http.NotFound(w, r)
			return
		}

		baseDir := "./dist"
		if cfg != nil && strings.TrimSpace(cfg.Server.AgentBinaryDir) != "" {
			baseDir = cfg.Server.AgentBinaryDir
		}
		cacheDir := filepath.Join(baseDir, ".external-cache")
		// 缓存文件名统一使用配置中的名称，避免大小写差异产生两份缓存。
		fileName := asset.FileName()
		cacheTarget := filepath.Join(cacheDir, fileName)
		if err := safepath.EnsureWithin(baseDir, cacheTarget); err != nil {
			http.NotFound(w, r)
			return
		}
		// 管理员上传的离线文件优先：平台无需出网即可提供老系统所需的 exe/脚本。
		localTarget := externalBinaryLocalPath(baseDir, *asset)
		if err := safepath.EnsureWithin(baseDir, localTarget); err != nil {
			http.NotFound(w, r)
			return
		}
		target := cacheTarget
		local := false
		if info, statErr := os.Stat(localTarget); statErr == nil && !info.IsDir() && info.Size() > 0 {
			target = localTarget
			local = true
		}

		ttl := effectiveExternalTTL(cfg)
		// local=1：只返回本地上传/已有缓存，绝不触发上游下载。
		// 老系统（PowerShell 2.0）用它快速判断平台是否已备好离线文件，
		// 避免平台现场下载 GitHub 导致 WebClient 超时。
		localOnly := r.URL.Query().Get("local") == "1" ||
			strings.EqualFold(r.Header.Get("X-External-Local-Only"), "1")
		cached := local || freshFile(target, ttl)
		if localOnly {
			if !local && !fileExists(target) {
				metrics.ExternalBinaryRequests.WithLabelValues(name, "local_miss").Inc()
				http.Error(w, "offline file not uploaded", http.StatusNotFound)
				return
			}
			// 本地优先；否则直接使用已有缓存（即使超过 TTL，也优于失败）。
			cached = true
		}
		if local {
			metrics.ExternalBinaryRequests.WithLabelValues(name, "local").Inc()
		} else if !cached {
			// 同一文件并发请求只下载一次：锁只覆盖下载阶段，拿到后立即释放，
			// 后续 ServeContent 流式输出不持锁（否则大文件会阻塞同文件的其它请求）。
			if fetchErr := downloadWithLock(r.Context(), target, *asset,
				effectiveExternalHosts(cfg), externalAllowRule(cfg),
				effectiveExternalProxy(cfg), effectiveExternalProxyHosts(cfg), ttl); fetchErr != nil {
				log.Printf("⚠️ 下载外部文件失败 %s (%s): %v", name, asset.URL, fetchErr)
				metrics.ExternalBinaryRequests.WithLabelValues(name, "error").Inc()
				httpx.Fail(w, http.StatusBadGateway,
					"控制台下载外部文件失败（请确认控制台能访问该地址）: "+fetchErr.Error())
				return
			}
			metrics.ExternalBinaryRequests.WithLabelValues(name, "fresh").Inc()
		} else {
			metrics.ExternalBinaryRequests.WithLabelValues(name, "cache_hit").Inc()
		}

		f, err := os.Open(target)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		contentType := "application/octet-stream"
		if asset.Script {
			contentType = "text/plain; charset=utf-8"
		}
		metrics.ExternalBinaryBytes.WithLabelValues(name).Add(float64(info.Size()))
		metrics.ExternalBinaryCacheAge.WithLabelValues(name).Set(time.Since(info.ModTime()).Seconds())

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", "attachment; filename="+fileName)

		// PowerShell 脚本必须带 UTF-8 BOM 再下发：
		// Windows Server 2008 R2 的 PowerShell 2.0 对无 BOM 的 .ps1 按系统 ANSI(GBK) 解码，
		// 中文注释/字符串会导致语法错误（UnexpectedToken）。这里在服务端统一补齐，
		// 浏览器、cmd、任务示例直接 & 下载文件等所有路径都能受益。
		// 只处理 .ps1/.psm1（BOM 会破坏 .sh/.py/.cmd）；大小在上传上限内，避免把大文件读进内存。
		if asset.Script && needsPowerShellBOM(*asset) && info.Size() <= externalBinaryScriptMaxSize {
			data, readErr := io.ReadAll(io.LimitReader(f, externalBinaryScriptMaxSize+1))
			if readErr == nil {
				data = bytes.TrimPrefix(data, utf8BOM)
				body := make([]byte, 0, len(data)+len(utf8BOM))
				body = append(body, utf8BOM...)
				body = append(body, data...)
				http.ServeContent(w, r, fileName, info.ModTime(), bytes.NewReader(body))
				return
			}
			log.Printf("⚠️ 读取脚本失败，按原样下发 %s: %v", name, readErr)
			if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
				http.Error(w, "read script failed", http.StatusInternalServerError)
				return
			}
		}
		http.ServeContent(w, r, fileName, info.ModTime(), f)
	}
}

// needsPowerShellBOM 判断脚本是否是 PowerShell（.ps1/.psm1）。
//
// 只给 PowerShell 脚本补 BOM：BOM 会破坏 Shell/Python 脚本和 .cmd/.bat。
// 先看白名单名称（客户端访问名），再看上游 URL 路径，兼容 name 不含扩展名的配置。
func needsPowerShellBOM(asset extbin.Entry) bool {
	for _, candidate := range []string{asset.Name, asset.URL} {
		path := candidate
		if u, err := neturl.Parse(candidate); err == nil && u.Path != "" {
			path = u.Path
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".ps1", ".psm1":
			return true
		}
	}
	return false
}

// fileExists 判断文件存在且非目录/非空。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// freshFile 判断缓存文件存在且未过期。
func freshFile(path string, ttl time.Duration) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return false
	}
	return time.Since(info.ModTime()) < ttl
}

// downloadWithLock 在文件锁内确认缓存并下载；锁仅覆盖下载阶段。
//
// 拿到锁后必须重新检查缓存：可能已有其它请求刚刚下载完成。
func downloadWithLock(ctx context.Context, target string, asset extbin.Entry,
	hosts []string, rule extbin.AllowRule, proxyBase string, proxyHosts []string, ttl time.Duration) error {
	unlock := lockExternalTarget(target)
	defer unlock()
	if freshFile(target, ttl) {
		return nil
	}
	start := time.Now()
	err := fetchExternalBinary(ctx, asset.URL, target, asset.Script, hosts, rule, proxyBase, proxyHosts)
	result := "success"
	if err != nil {
		result = "error"
	}
	metrics.ExternalBinaryDuration.WithLabelValues(asset.Name, result).
		Observe(time.Since(start).Seconds())
	return err
}

// fetchExternalBinary 下载到临时文件再原子替换，避免并发/中断留下半个文件。
//
// 下载顺序：加速代理优先，失败后直连兜底。代理地址和代理域名由设置页配置；
// 内网镜像不在 proxyHosts 内时保持直连，避免把内网地址发给公网代理。
// hosts 是允许的上游域名白名单；下载前同时校验域名与目标 IP（拒绝内网），
// 防止这项能力被当作 SSRF 跳板。
func fetchExternalBinary(ctx context.Context, source, target string, script bool, hosts []string,
	rule extbin.AllowRule, proxyBase string, proxyHosts []string) error {
	if err := extbin.CheckHost(source, hosts, rule); err != nil {
		return err
	}
	proxied, viaProxy := extbin.ProxyURL(source, proxyBase, proxyHosts)
	if !viaProxy {
		return fetchExternalBinaryOnce(ctx, source, target, script, hosts, rule)
	}

	proxyErr := fetchExternalBinaryOnce(ctx, proxied, target, script,
		hostsWithProxyHost(hosts, proxyBase), rule)
	if proxyErr == nil {
		return nil
	}
	// 代理不可用时仍保留直连兜底；国内通常直连也会失败，但海外/专线环境可用。
	if directErr := fetchExternalBinaryOnce(ctx, source, target, script, hosts, rule); directErr != nil {
		return fmt.Errorf("经加速代理下载失败: %v；直连兜底也失败: %v", proxyErr, directErr)
	}
	log.Printf("⚠️ 加速代理下载失败，已直连兜底 %s: %v", source, proxyErr)
	return nil
}

func fetchExternalBinaryOnce(ctx context.Context, source, target string, script bool, hosts []string, rule extbin.AllowRule) error {
	// 域名白名单 + 内网策略：页面「测试下载」使用同一函数，保证测通即可下载。
	if err := extbin.CheckHost(source, hosts, rule); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "upgrade-mgr/external-binary-cache")
	// 每一跳重定向都重新校验：白名单域名可能 302 到内网地址（SSRF 绕过）。
	client := &http.Client{
		Timeout:   externalBinaryClient.Timeout,
		Transport: externalBinaryClient.Transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向次数过多")
			}
			if err := extbin.CheckHost(next.URL.String(), hosts, rule); err != nil {
				return fmt.Errorf("重定向目标被拒绝: %w", err)
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("源站返回 HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	n, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, externalBinaryMaxSize+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > externalBinaryMaxSize {
		return fmt.Errorf("文件超过上限 %d 字节", externalBinaryMaxSize)
	}
	if err := validateExternalContent(tmpPath, script); err != nil {
		return err
	}
	return os.Rename(tmpPath, target)
}

// validateExternalContent 校验下载内容：二进制必须是 PE（MZ）或 ELF，脚本不能是 HTML 错误页。
func validateExternalContent(path string, script bool) error {
	head := make([]byte, 64)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	n, _ := f.Read(head)
	f.Close()
	if n == 0 {
		return fmt.Errorf("下载内容为空")
	}
	// 带 BOM 的脚本也要能识别 HTML 错误页（BOM 不是空白字符）。
	head = bytes.TrimPrefix(head[:n], utf8BOM)
	if len(head) == 0 {
		return fmt.Errorf("下载内容为空")
	}
	sample := strings.TrimSpace(strings.ToLower(string(head)))
	if script {
		for _, prefix := range []string{"<!doctype", "<html", "<head", "<body", "<?xml"} {
			if strings.HasPrefix(sample, prefix) {
				return fmt.Errorf("下载内容不是脚本，而是 HTML/XML 页面（可能是错误页）")
			}
		}
		return nil
	}
	if !extbin.IsExecutable(head) {
		return fmt.Errorf("下载内容不是可执行文件（缺少 PE/ELF 头，可能是错误页）")
	}
	return nil
}

// externalCacheMu 串行化同一文件的并发下载，避免重复拉取与半成品覆盖。
//
// 使用 keyedlock 而非 sync.Map：key 用完自动清理，长期运行不会累积。
var externalCacheMu = keyedlock.New()

// lockExternalTarget 返回目标文件的下载锁。
func lockExternalTarget(target string) func() { return externalCacheMu.Lock(target) }
