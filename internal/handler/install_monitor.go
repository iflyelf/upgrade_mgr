package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/examples"
	"github.com/iflyelf/upgrade_mgr/internal/auth"
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

func readInstallMonitorToken(s *svc.ServiceContext) (*installMonitorTokenFile, error) {
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

func writeInstallMonitorToken(s *svc.ServiceContext, f installMonitorTokenFile) error {
	path, err := installMonitorTokenPath(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
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
		old, _ := readInstallMonitorToken(s)
		token, prefix := newAutomationToken()
		id, createdAt, err := insertAutomationToken(r.Context(), s.DB, token, prefix,
			tenantID, "install-monitor", "一体化安装脚本（永久）", creator, nil, 0, nil)
		if err != nil {
			auditOpDetails(s, r, audit.ActionEnrollToken, "install_monitor", "", "install-monitor", err, nil, nil)
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := writeInstallMonitorToken(s, installMonitorTokenFile{
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

		snap := s.ConfigSnapshot()
		base := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
		httpx.OK(w, map[string]any{
			"id":             id,
			"token_prefix":   prefix + "…",
			"created_at":     createdAt,
			"base_url":       base,
			"linux_url":      base + "/api/external/binary/install-monitor.sh",
			"windows_url":    base + "/api/external/binary/install-windows-monitor.exe",
			"token_endpoint": base + "/api/agent/install-monitor/token",
		})
	}
}

// InstallMonitorTokenGet 返回当前永久令牌（无鉴权，供内网安装脚本读取）。
//
// 安全边界：该接口与外部文件下载一样假设控制台部署在内网；不要直接暴露到公网。
func InstallMonitorTokenGet(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := readInstallMonitorToken(s)
		if err != nil {
			httpx.Fail(w, http.StatusNotFound, "尚未生成接入脚本，请先在控制台「接入主机 → 生成接入脚本」生成")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(f.Token + "\n"))
	}
}

// InstallMonitorScript 下发一体化 Linux 安装脚本（无鉴权，内容为公开脚本）。
func InstallMonitorScript(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := examples.EmbeddedScript("install-monitor.sh")
		if len(data) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		_, _ = w.Write(data)
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
