package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/safepath"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// installMonitorTokenFile 是控制台为一体化安装脚本保存的永久令牌。
//
// 脚本本身是静态的（/api/external/binary/install-monitor.sh），运行时再读取
// 这里的 token；因此重新生成/轮换令牌后，旧脚本 URL 仍然有效。
type installMonitorTokenFile struct {
	ID        int64     `json:"id"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

func installMonitorBaseDir(s *svc.ServiceContext) string {
	dir := "./dist"
	if s != nil {
		cfg := s.ConfigSnapshot()
		if v := strings.TrimSpace(cfg.Server.AgentBinaryDir); v != "" {
			dir = v
		}
	}
	return dir
}

func installMonitorTokenPath(s *svc.ServiceContext) (string, error) {
	base := installMonitorBaseDir(s)
	target := filepath.Join(base, ".install-monitor", "token.json")
	if err := safepath.EnsureWithin(base, target); err != nil {
		return "", err
	}
	return target, nil
}

// installMonitorTokenSettingKey 永久安装令牌的数据库存储键。
//
// 存 app_settings 而不是只写本地文件：多副本部署时，生成令牌的副本与脚本
// 读取令牌的副本可能不是同一个实例，本地文件会读不到（提示「未获取到接入令牌」）。
var installMonitorTokenSettingKey = "agent.install_monitor_token"

// installMonitorTokenMu 串行化「首次自动创建」，避免并发脚本同时生成多个令牌。
var installMonitorTokenMu sync.Mutex

func readInstallMonitorToken(ctx context.Context, s *svc.ServiceContext) (*installMonitorTokenFile, error) {
	// 优先读数据库（多副本共享）；旧版本的本地文件作为兼容回退。
	if s != nil && s.DB != nil {
		var raw string
		err := s.DB.QueryRowContext(ctx,
			`SELECT value FROM app_settings WHERE key=$1`, installMonitorTokenSettingKey).Scan(&raw)
		if err == nil && strings.TrimSpace(raw) != "" {
			var f installMonitorTokenFile
			if json.Unmarshal([]byte(raw), &f) == nil && strings.TrimSpace(f.Token) != "" {
				return &f, nil
			}
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("⚠️ 读取安装令牌设置失败（回退本地文件）: %v", err)
		}
	}

	path, err := installMonitorTokenPath(s)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f installMonitorTokenFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if strings.TrimSpace(f.Token) == "" {
		return nil, errors.New("install-monitor token 为空")
	}
	return &f, nil
}

func writeInstallMonitorToken(ctx context.Context, s *svc.ServiceContext, f installMonitorTokenFile) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if s == nil || s.DB == nil {
		return errors.New("数据库不可用，无法保存接入令牌")
	}
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,NOW())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()`,
		installMonitorTokenSettingKey, string(data)); err != nil {
		return err
	}
	// 本地文件只作为兼容缓存：写入失败不影响多副本通过数据库读取。
	if path, err := installMonitorTokenPath(s); err == nil {
		if werr := writeTokenFile(path, data); werr != nil {
			log.Printf("⚠️ 写入安装令牌文件失败（不影响使用）: %v", werr)
		}
	}
	return nil
}

func writeTokenFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".token-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// installMonitorTokenUsable 校验存储的令牌是否仍可消费（未撤销/未过期/未用尽）。
//
// 页面可能撤销过旧令牌；此时自动重建，避免脚本长期收到不可用的令牌。
func installMonitorTokenUsable(ctx context.Context, s *svc.ServiceContext, f *installMonitorTokenFile) bool {
	if f == nil || strings.TrimSpace(f.Token) == "" {
		return false
	}
	if s == nil || s.DB == nil {
		return true
	}
	var ok bool
	err := s.DB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM enroll_tokens
			 WHERE token_hash=$1 AND token_type='automation' AND enabled
			   AND (expires_at IS NULL OR expires_at > NOW())
			   AND (max_uses=0 OR used_count < max_uses)
		)`, agentgw.HashToken(f.Token)).Scan(&ok)
	return err == nil && ok
}

// ensureInstallMonitorToken 返回可用的永久安装令牌；不存在或已失效时自动创建。
//
// 首次执行安装脚本时用户可能还没在页面点「生成接入脚本」，这里自动准备，
// 让自动化脚本无需任何人工干预。创建过程加锁，避免并发脚本生成多个令牌。
func ensureInstallMonitorToken(ctx context.Context, s *svc.ServiceContext) (*installMonitorTokenFile, error) {
	if f, err := readInstallMonitorToken(ctx, s); err == nil && installMonitorTokenUsable(ctx, s, f) {
		return f, nil
	}

	installMonitorTokenMu.Lock()
	defer installMonitorTokenMu.Unlock()
	// 拿到锁后复查：其它请求可能已经创建完成。
	if f, err := readInstallMonitorToken(ctx, s); err == nil && installMonitorTokenUsable(ctx, s, f) {
		return f, nil
	}
	if s == nil || s.DB == nil {
		return nil, errors.New("数据库不可用，无法自动创建接入令牌")
	}

	tenantID := int64(1)
	_ = s.DB.QueryRowContext(ctx, `SELECT id FROM tenants ORDER BY id LIMIT 1`).Scan(&tenantID)

	token, prefix := newAutomationToken()
	id, createdAt, err := insertAutomationToken(ctx, s.DB, token, prefix, tenantID,
		"install-monitor", "一体化安装脚本（首次执行自动创建）", "system", nil, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("自动创建接入令牌失败: %w", err)
	}
	f := &installMonitorTokenFile{ID: id, Token: token, CreatedAt: createdAt}
	if err := writeInstallMonitorToken(ctx, s, *f); err != nil {
		_, _ = s.DB.ExecContext(ctx, `
			UPDATE enroll_tokens SET enabled=false, revoked_at=NOW(), revoked_by='system'
			 WHERE id=$1`, id)
		return nil, fmt.Errorf("保存接入令牌失败: %w", err)
	}
	log.Printf("ℹ️ 已自动创建一体化安装脚本令牌（id=%d），脚本执行无需人工生成", id)
	return f, nil
}

// installMonitorPublicInfo 返回页面展示所需的安装脚本信息（不含完整令牌）。
func installMonitorPublicInfo(s *svc.ServiceContext, r *http.Request, f *installMonitorTokenFile) map[string]any {
	snap := s.ConfigSnapshot()
	base := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
	prefix := f.Token
	if len(prefix) > automationTokenPrefixKeep {
		prefix = prefix[:automationTokenPrefixKeep]
	}
	return map[string]any{
		"id":             f.ID,
		"token_prefix":   prefix + "…",
		"created_at":     f.CreatedAt,
		"base_url":       base,
		"linux_url":      base + "/api/external/binary/install-monitor.sh",
		"windows_url":    base + "/api/external/binary/install-windows-monitor.exe",
		"token_endpoint": base + "/api/agent/install-monitor/token",
	}
}

// InstallMonitorTokenCreate 生成/轮换一体化安装脚本的永久自动化令牌（管理员）。
//
// 轮换时旧令牌立即失效；脚本 URL 不变，下次执行会自动读取新令牌。
func InstallMonitorTokenCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		tenantID := u.TenantID
		if tenantID <= 0 {
			tenantID = 1
		}
		creator := u.Username
		if creator == "" {
			creator = "admin"
		}

		// 先生成新令牌并落盘，成功后再注销旧令牌：
		// 任一步失败时，脚本仍能读取到原来的有效令牌。
		old, _ := readInstallMonitorToken(r.Context(), s)
		token, prefix := newAutomationToken()
		id, createdAt, err := insertAutomationToken(r.Context(), s.DB, token, prefix,
			tenantID, "install-monitor", "一体化安装脚本（永久）", creator, nil, 0, nil)
		if err != nil {
			auditOpDetails(s, r, audit.ActionEnrollToken, "install_monitor", "", "install-monitor", err, nil, nil)
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := writeInstallMonitorToken(r.Context(), s, installMonitorTokenFile{
			ID: id, Token: token, CreatedAt: createdAt,
		}); err != nil {
			// 新令牌未落盘：立即注销，避免留下不可用的永久令牌。
			_, _ = s.DB.ExecContext(r.Context(), `
				UPDATE enroll_tokens SET enabled=false, revoked_at=NOW(), revoked_by=$2
				 WHERE id=$1`, id, creator)
			auditOpDetails(s, r, audit.ActionEnrollToken, "install_monitor", "", "install-monitor", err, nil, nil)
			httpx.Fail(w, http.StatusInternalServerError, "写入安装令牌失败: "+err.Error())
			return
		}
		if old != nil && old.ID > 0 && old.ID != id {
			_, _ = s.DB.ExecContext(r.Context(), `
				UPDATE enroll_tokens
				   SET enabled=false, revoked_at=NOW(), revoked_by=$2
				 WHERE id=$1 AND token_type='automation' AND tenant_id=$3`,
				old.ID, creator, tenantID)
		}
		auditOpDetails(s, r, audit.ActionEnrollToken, "install_monitor",
			strconv.FormatInt(id, 10), "install-monitor", nil, map[string]any{
				"token_type": "automation", "permanent": true,
			}, nil)

		httpx.OK(w, installMonitorPublicInfo(s, r, &installMonitorTokenFile{
			ID: id, Token: token, CreatedAt: createdAt,
		}))
	}
}

// InstallMonitorTokenGet 返回当前永久令牌（无鉴权，供内网安装脚本读取）。
//
// 令牌不存在时自动创建，保证安装脚本在未点过「生成接入脚本」时也能直接执行，
// 无需任何人工干预。安全边界：该接口与外部文件下载一样假设控制台部署在内网；
// 不要直接暴露到公网。
func InstallMonitorTokenGet(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := ensureInstallMonitorToken(r.Context(), s)
		if err != nil {
			log.Printf("⚠️ 自动准备安装令牌失败: %v", err)
			httpx.Fail(w, http.StatusInternalServerError, "无法自动准备接入令牌: "+err.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		// ?format=json 供页面自动展示脚本命令；默认仍是脚本读取的纯文本令牌。
		if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "json") {
			httpx.OK(w, installMonitorPublicInfo(s, r, f))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(f.Token + "\n"))
	}
}

// InstallWindowsMonitorExe 下发 Windows 一体化安装器。
//
// 二进制由 `make build-monitor-installer` 生成到 server.agent_binary_dir；
// 未构建时返回 404 并提示，不影响其它功能。
func InstallWindowsMonitorExe(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := installMonitorBaseDir(s)
		target := filepath.Join(base, "install-windows-monitor.exe")
		if err := safepath.EnsureWithin(base, target); err != nil {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(target)
		if err != nil {
			httpx.Fail(w, http.StatusNotFound,
				"Windows 安装器尚未构建：请在服务器执行 make build-monitor-installer，并确认文件位于 "+target)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
		w.Header().Set("Content-Disposition", "attachment; filename=install-windows-monitor.exe")
		http.ServeContent(w, r, "install-windows-monitor.exe", info.ModTime(), f)
	}
}
