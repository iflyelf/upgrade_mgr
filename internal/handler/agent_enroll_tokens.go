package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

const (
	automationTokenPrefix     = "enr_auto_"
	maxAutomationTokenUses    = 1_000_000
	maxAutomationTokenCIDRs   = 100
	maxAutomationTokenName    = 100
	maxAutomationTokenNote    = 255
	automationTokenPrefixKeep = 20
)

// automationTokenCreateRequest 自动化注册令牌创建参数。
type automationTokenCreateRequest struct {
	Name         string   `json:"name"`
	TenantID     int64    `json:"tenant_id,omitempty"`
	ExpiresIn    string   `json:"expires_in,omitempty"` // 如 24h / 720h；permanent 也可
	ExpiresAt    string   `json:"expires_at,omitempty"` // RFC3339，自定义到期时间
	Permanent    bool     `json:"permanent,omitempty"`  // true 表示永不过期
	MaxUses      int      `json:"max_uses"`             // 0=不限制
	AllowedCIDRs []string `json:"allowed_cidrs"`        // 空=不限制来源 IP
	OS           string   `json:"os"`                   // 仅用于生成安装命令
	Arch         string   `json:"arch"`                 // 仅用于生成安装命令（兼容旧客户端，可逗号分隔）
	Arches       []string `json:"arches,omitempty"`     // 安装命令目标架构（可多选）
	Force        bool     `json:"force"`                // 安装命令是否带 --force
	Note         string   `json:"note"`
}

// agentInstallCommand 单个架构的 Agent 安装命令。
// 一次性令牌会为每个架构分别生成独立令牌；自动化令牌复用同一个令牌。
type agentInstallCommand struct {
	Arch           string `json:"arch"`
	EnrollToken    string `json:"enroll_token"`
	InstallCommand string `json:"install_command"`
}

// maxEnrollArches 单次生成安装命令的架构数上限，避免一次请求创建过多令牌。
const maxEnrollArches = 10

// parseEnrollArches 解析逗号/空白分隔的架构列表，去重并校验 OS+架构组合。
func parseEnrollArches(osName, raw string) ([]string, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		raw = "amd64"
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		if !isValidTarget(osName, p) {
			return nil, fmt.Errorf("不支持的操作系统或架构: %s/%s", osName, p)
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		if !isValidTarget(osName, "amd64") {
			return nil, fmt.Errorf("不支持的操作系统或架构: %s/amd64", osName)
		}
		out = append(out, "amd64")
	}
	if len(out) > maxEnrollArches {
		return nil, fmt.Errorf("一次最多选择 %d 个架构", maxEnrollArches)
	}
	return out, nil
}

// automationTokenDB 同时适配 *sql.DB 与 *sql.Tx。
type automationTokenDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// AutomationEnrollTokenCreate 创建自动化注册令牌（管理员）。
//
// 与一次性令牌不同：automation 令牌可以永久有效或自定义到期时间，
// 可设置使用次数和来源 CIDR，并且可以在页面注销；一次性令牌逻辑不变。
func AutomationEnrollTokenCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req automationTokenCreateRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		tenantID, err := resolveAutomationTenant(r, req.TenantID)
		if err != nil {
			httpx.Fail(w, http.StatusForbidden, err.Error())
			return
		}
		if err := ensureTenantExists(r.Context(), s.DB, tenantID); err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		expiresAt, err := resolveAutomationExpiry(req)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.MaxUses < 0 || req.MaxUses > maxAutomationTokenUses {
			httpx.Fail(w, http.StatusBadRequest,
				fmt.Sprintf("最大使用次数应为 0-%d（0 表示不限制）", maxAutomationTokenUses))
			return
		}
		cidrs, err := normalizeAutomationCIDRs(req.AllowedCIDRs)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		name := cleanAgentText(req.Name, maxAutomationTokenName)
		if name == "" {
			name = "automation"
		}
		note := cleanAgentText(req.Note, maxAutomationTokenNote)
		creator := ""
		if u, ok := auth.FromContext(r.Context()); ok && u != nil {
			creator = u.Username
		}

		token, prefix := newAutomationToken()
		osName, arches, err := normalizeAutomationTargets(req.OS, req.Arch, req.Arches)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		commands, baseURL, err := buildAutomationInstallCommands(s, r, token, osName, arches, req.Force)
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "生成安装命令失败")
			return
		}

		id, createdAt, err := insertAutomationToken(r.Context(), s.DB, token, prefix,
			tenantID, name, note, creator, expiresAt, req.MaxUses, cidrs)
		if err != nil {
			auditOpDetails(s, r, audit.ActionEnrollToken, "enroll_token", "", name, err, nil, nil)
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		auditOpDetails(s, r, audit.ActionEnrollToken, "enroll_token",
			strconv.FormatInt(id, 10), name, nil, map[string]any{
				"token_type": "automation", "tenant_id": tenantID,
				"expires_at": expiresAt, "max_uses": req.MaxUses,
				"allowed_cidrs": cidrs, "arches": arches,
			}, nil)
		httpx.OK(w, automationTokenCreatedResponse{
			ID: id, Token: token, TokenPrefix: prefix, Name: name, TenantID: tenantID,
			ExpiresAt: expiresAt, MaxUses: req.MaxUses, AllowedCIDRs: cidrs,
			OS: osName, Arch: commands[0].Arch, Arches: arches,
			InstallCommand: commands[0].InstallCommand, Commands: commands,
			BaseURL: baseURL, CreatedAt: createdAt,
		})
	}
}

// AutomationEnrollTokenList 列出自动化注册令牌（不返回完整 token）。
func AutomationEnrollTokenList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		tenantSQL := ""
		args := []any{}
		requested := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
		if !u.IsSuperAdmin() {
			tid := u.TenantID
			if tid <= 0 {
				tid = 1
			}
			tenantSQL = " AND tenant_id=$1"
			args = append(args, tid)
		} else if requested != "" {
			tid, err := strconv.ParseInt(requested, 10, 64)
			if err != nil || tid <= 0 {
				httpx.Fail(w, http.StatusBadRequest, "租户 ID 非法")
				return
			}
			tenantSQL = " AND tenant_id=$1"
			args = append(args, tid)
		}

		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT id, tenant_id, name, token_prefix, expires_at, max_uses, used_count,
			       enabled, revoked_at, COALESCE(revoked_by,''), allowed_cidrs,
			       last_used_at, COALESCE(last_used_ip,''), COALESCE(created_by,''), created_at, COALESCE(note,'')
			  FROM enroll_tokens
			 WHERE token_type='automation'`+tenantSQL+`
			 ORDER BY created_at DESC
			 LIMIT 500`, args...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()

		list := make([]map[string]any, 0, 16)
		now := time.Now()
		for rows.Next() {
			var (
				id, tenantID, maxUses, usedCount    int64
				name, prefix, revokedBy, lastUsedIP string
				createdBy, note                     string
				enabled                             bool
				expiresAt, revokedAt, lastUsedAt    sql.NullTime
				cidrs                               []string
				createdAt                           time.Time
			)
			if err := rows.Scan(&id, &tenantID, &name, &prefix, &expiresAt, &maxUses, &usedCount,
				&enabled, &revokedAt, &revokedBy, pq.Array(&cidrs),
				&lastUsedAt, &lastUsedIP, &createdBy, &createdAt, &note); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			status := "active"
			switch {
			case !enabled || revokedAt.Valid:
				status = "revoked"
			case expiresAt.Valid && !expiresAt.Time.After(now):
				status = "expired"
			case maxUses > 0 && usedCount >= maxUses:
				status = "exhausted"
			}
			if cidrs == nil {
				cidrs = []string{}
			}
			list = append(list, map[string]any{
				"id": id, "name": name, "token_prefix": prefix,
				"tenant_id": tenantID, "status": status,
				"expires_at": nullableTime(expiresAt), "max_uses": maxUses,
				"used_count": usedCount, "enabled": enabled,
				"revoked_at": nullableTime(revokedAt), "revoked_by": revokedBy,
				"allowed_cidrs": cidrs, "last_used_at": nullableTime(lastUsedAt),
				"last_used_ip": lastUsedIP, "created_by": createdBy,
				"created_at": createdAt, "note": note,
			})
		}
		if err := rows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{"list": list, "total": len(list)})
	}
}

// AutomationEnrollTokenRevoke 注销自动化令牌；只阻止后续注册，不影响已注册 Agent。
func AutomationEnrollTokenRevoke(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "令牌 ID 非法")
			return
		}
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		args := []any{id, u.Username}
		tenantSQL := ""
		if !u.IsSuperAdmin() {
			tid := u.TenantID
			if tid <= 0 {
				tid = 1
			}
			tenantSQL = " AND tenant_id=$3"
			args = append(args, tid)
		}
		var name string
		err = s.DB.QueryRowContext(r.Context(), `
			UPDATE enroll_tokens
			SET enabled=false,
			    revoked_at=COALESCE(revoked_at,NOW()),
			    revoked_by=CASE WHEN COALESCE(revoked_by,'')='' THEN $2 ELSE revoked_by END
			WHERE id=$1 AND token_type='automation'`+tenantSQL+`
			RETURNING name`, args...).Scan(&name)
		if err == sql.ErrNoRows {
			httpx.Fail(w, http.StatusNotFound, "自动化令牌不存在")
			return
		}
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		auditOp(s, r, audit.ActionEnrollTokenRevoke, "enroll_token", strconv.FormatInt(id, 10), name, nil)
		httpx.OK(w, map[string]any{"revoked": true, "id": id})
	}
}

// AutomationEnrollTokenRotate 轮换自动化令牌：旧 token 立即注销，新 token 沿用原策略。
//
// os/arch/force 仅用于生成返回的安装命令，不影响 token 本身。
func AutomationEnrollTokenRotate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "令牌 ID 非法")
			return
		}
		osName, arches, err := normalizeAutomationTargets(r.URL.Query().Get("os"), r.URL.Query().Get("arch"), nil)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		force := r.URL.Query().Get("force") == "1"
		token, prefix := newAutomationToken()
		commands, baseURL, err := buildAutomationInstallCommands(s, r, token, osName, arches, force)
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "生成安装命令失败")
			return
		}

		tx, err := s.DB.BeginTx(r.Context(), nil)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer tx.Rollback()

		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		tenantSQL := ""
		args := []any{id}
		if !u.IsSuperAdmin() {
			tid := u.TenantID
			if tid <= 0 {
				tid = 1
			}
			tenantSQL = " AND tenant_id=$2"
			args = append(args, tid)
		}

		var (
			tenantID              int64
			name, note, createdBy string
			maxUses               int
			enabled               bool
			expiresAt, revokedAt  sql.NullTime
			cidrs                 []string
		)
		err = tx.QueryRowContext(r.Context(), `
			SELECT tenant_id, name, expires_at, max_uses, allowed_cidrs, COALESCE(note,''),
			       enabled, revoked_at, COALESCE(created_by,'')
			  FROM enroll_tokens
			 WHERE id=$1 AND token_type='automation'`+tenantSQL+`
			 FOR UPDATE`, args...).Scan(&tenantID, &name, &expiresAt, &maxUses,
			pq.Array(&cidrs), &note, &enabled, &revokedAt, &createdBy)
		if err == sql.ErrNoRows {
			httpx.Fail(w, http.StatusNotFound, "自动化令牌不存在")
			return
		}
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		if !enabled || revokedAt.Valid {
			httpx.Fail(w, http.StatusBadRequest, "令牌已注销，请新建自动化令牌")
			return
		}
		if expiresAt.Valid && !expiresAt.Time.After(time.Now()) {
			httpx.Fail(w, http.StatusBadRequest, "令牌已过期，请新建自动化令牌并设置新的有效期")
			return
		}
		var newExpiresAt *time.Time
		if expiresAt.Valid {
			t := expiresAt.Time
			newExpiresAt = &t
		}
		newID, newCreatedAt, err := insertAutomationToken(r.Context(), tx, token, prefix,
			tenantID, name, note, createdBy, newExpiresAt, maxUses, cidrs)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `
			UPDATE enroll_tokens
			SET enabled=false, revoked_at=NOW(), revoked_by=$2
			WHERE id=$1`, id, u.Username); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := tx.Commit(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		auditOp(s, r, audit.ActionEnrollTokenRotate, "enroll_token",
			strconv.FormatInt(id, 10), name, nil)
		httpx.OK(w, automationTokenCreatedResponse{
			ID: newID, Token: token, TokenPrefix: prefix, Name: name, TenantID: tenantID,
			ExpiresAt: newExpiresAt, MaxUses: maxUses, AllowedCIDRs: cidrs,
			OS: osName, Arch: commands[0].Arch, Arches: arches,
			InstallCommand: commands[0].InstallCommand, Commands: commands,
			BaseURL: baseURL, CreatedAt: newCreatedAt,
		})
	}
}

type automationTokenCreatedResponse struct {
	ID             int64                 `json:"id"`
	Token          string                `json:"token"`
	TokenPrefix    string                `json:"token_prefix"`
	Name           string                `json:"name"`
	TenantID       int64                 `json:"tenant_id"`
	ExpiresAt      *time.Time            `json:"expires_at"`
	MaxUses        int                   `json:"max_uses"`
	AllowedCIDRs   []string              `json:"allowed_cidrs"`
	OS             string                `json:"os"`
	Arch           string                `json:"arch"`
	Arches         []string              `json:"arches"`
	InstallCommand string                `json:"install_command"`
	Commands       []agentInstallCommand `json:"commands"`
	BaseURL        string                `json:"base_url"`
	CreatedAt      time.Time             `json:"created_at"`
}

func resolveAutomationTenant(r *http.Request, requested int64) (int64, error) {
	u, ok := auth.FromContext(r.Context())
	if !ok || u == nil {
		return 0, fmt.Errorf("未认证")
	}
	if u.IsSuperAdmin() {
		if requested > 0 {
			return requested, nil
		}
		if u.TenantID > 0 {
			return u.TenantID, nil
		}
		return 1, nil
	}
	tid := u.TenantID
	if tid <= 0 {
		tid = 1
	}
	if requested > 0 && requested != tid {
		return 0, fmt.Errorf("无权操作其他租户")
	}
	return tid, nil
}

func ensureTenantExists(ctx context.Context, db *sql.DB, tenantID int64) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1)`, tenantID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("租户不存在")
	}
	return nil
}

func resolveAutomationExpiry(req automationTokenCreateRequest) (*time.Time, error) {
	expiresIn := strings.TrimSpace(req.ExpiresIn)
	expiresAtRaw := strings.TrimSpace(req.ExpiresAt)
	permanent := req.Permanent || strings.EqualFold(expiresIn, "permanent")
	if permanent {
		if expiresAtRaw != "" || (expiresIn != "" && !strings.EqualFold(expiresIn, "permanent")) {
			return nil, fmt.Errorf("永久令牌不能同时设置 expires_in/expires_at")
		}
		return nil, nil
	}
	if expiresIn != "" && expiresAtRaw != "" {
		return nil, fmt.Errorf("expires_in 与 expires_at 只能二选一")
	}
	now := time.Now()
	if expiresAtRaw != "" {
		t, err := time.Parse(time.RFC3339, expiresAtRaw)
		if err != nil {
			return nil, fmt.Errorf("expires_at 必须是 RFC3339 时间（如 2026-12-31T23:59:59+08:00）")
		}
		if !t.After(now) {
			return nil, fmt.Errorf("到期时间必须晚于当前时间")
		}
		return &t, nil
	}
	if expiresIn != "" {
		d, err := time.ParseDuration(expiresIn)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("expires_in 必须是正时长（如 24h / 720h），永久请用 permanent")
		}
		t := now.Add(d)
		return &t, nil
	}
	return nil, fmt.Errorf("请指定有效期：expires_in、expires_at 或 permanent")
}

func normalizeAutomationCIDRs(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("CIDR 格式错误: %s", item)
		}
		key := ipnet.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
		if len(out) > maxAutomationTokenCIDRs {
			return nil, fmt.Errorf("CIDR 最多 %d 条", maxAutomationTokenCIDRs)
		}
	}
	return out, nil
}

// normalizeAutomationTargets 解析自动化令牌的安装命令目标。
// arches 优先；兼容旧的 arch 字符串（可逗号分隔）。
func normalizeAutomationTargets(osName, arch string, arches []string) (string, []string, error) {
	osName = strings.TrimSpace(strings.ToLower(osName))
	if osName == "" {
		osName = "linux"
	}
	raw := arch
	if len(arches) > 0 {
		raw = strings.Join(arches, ",")
	}
	list, err := parseEnrollArches(osName, raw)
	if err != nil {
		return "", nil, err
	}
	return osName, list, nil
}

func newAutomationToken() (token, prefix string) {
	token = automationTokenPrefix + randHex(32)
	prefix = token
	if len(prefix) > automationTokenPrefixKeep {
		prefix = prefix[:automationTokenPrefixKeep]
	}
	return token, prefix
}

func insertAutomationToken(ctx context.Context, db automationTokenDB, token, prefix string,
	tenantID int64, name, note, creator string, expiresAt *time.Time, maxUses int, cidrs []string) (int64, time.Time, error) {
	var expiresArg any
	if expiresAt != nil {
		expiresArg = *expiresAt
	}
	if cidrs == nil {
		cidrs = []string{}
	}
	var id int64
	var createdAt time.Time
	err := db.QueryRowContext(ctx, `
		INSERT INTO enroll_tokens
			(token_hash, token_prefix, token_type, expires_at, max_uses, used_count,
			 enabled, allowed_cidrs, created_by, tenant_id, name, note)
		VALUES ($1, $2, 'automation', $3, $4, 0, TRUE, $5, $6, $7, $8, $9)
		RETURNING id, created_at`,
		agentgw.HashToken(token), prefix, expiresArg, maxUses,
		pq.Array(cidrs), creator, tenantID, name, note).Scan(&id, &createdAt)
	if err != nil {
		return 0, time.Time{}, err
	}
	return id, createdAt, nil
}

func buildAutomationInstallCommands(s *svc.ServiceContext, r *http.Request, token, osName string, arches []string, force bool) ([]agentInstallCommand, string, error) {
	commands := make([]agentInstallCommand, 0, len(arches))
	baseURL := ""
	for _, arch := range arches {
		cmd, base, err := buildAutomationInstallCommand(s, r, token, osName, arch, force)
		if err != nil {
			return nil, "", err
		}
		baseURL = base
		commands = append(commands, agentInstallCommand{
			Arch: arch, EnrollToken: token, InstallCommand: cmd,
		})
	}
	return commands, baseURL, nil
}

func buildAutomationInstallCommand(s *svc.ServiceContext, r *http.Request, token, osName, arch string, force bool) (string, string, error) {
	snap := s.ConfigSnapshot()
	serverURL := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
	rawURL, err := validateURL(serverURL)
	if err != nil {
		return "", "", err
	}
	safeURL := shellEscape(rawURL)
	if osName == "windows" {
		return buildWindowsInstallCommand(rawURL, arch, token, force), serverURL, nil
	}
	// 自动化接入时不支持 variant 参数，默认生成普通构建的安装命令
	return buildInstallCommand(safeURL, arch, token, force, ""), serverURL, nil
}

// AutomationEnrollTokenBatchDelete 批量删除自动化令牌：POST /api/agent/enroll-tokens/batch-delete。
//
// 删除会直接从列表移除；有效令牌删除等同于立即注销，只阻止后续注册。
func AutomationEnrollTokenBatchDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		var req struct {
			IDs []int64 `json:"ids"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil || len(req.IDs) == 0 {
			httpx.Fail(w, http.StatusBadRequest, "请提供令牌 ID 列表")
			return
		}
		ids := uniquePositiveIDs(req.IDs)
		if len(ids) > 200 {
			httpx.Fail(w, http.StatusBadRequest, "单次最多删除 200 个令牌")
			return
		}
		deleted := []int64{}
		failed := []map[string]any{}
		for _, id := range ids {
			args := []any{id}
			tenantSQL := ""
			if !u.IsSuperAdmin() {
				tid := u.TenantID
				if tid <= 0 {
					tid = 1
				}
				tenantSQL = " AND tenant_id=$2"
				args = append(args, tid)
			}
			res, err := s.DB.ExecContext(r.Context(),
				`DELETE FROM enroll_tokens WHERE id=$1 AND token_type='automation'`+tenantSQL, args...)
			if err != nil {
				failed = append(failed, map[string]any{"id": id, "error": err.Error()})
				continue
			}
			if n, _ := res.RowsAffected(); n == 0 {
				failed = append(failed, map[string]any{"id": id, "error": "令牌不存在"})
				continue
			}
			deleted = append(deleted, id)
		}
		if len(deleted) > 0 {
			auditOpDetails(s, r, audit.ActionEnrollTokenDelete, "enroll_token_batch_delete",
				"", "", nil, map[string]any{"deleted": deleted, "failed_count": len(failed)}, nil)
		}
		httpx.OK(w, map[string]any{"deleted": deleted, "failed": failed})
	}
}
