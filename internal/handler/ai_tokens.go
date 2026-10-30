package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/aitoken"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// aiTokenPolicy 由当前设置快照构造令牌策略（页面保存后立即生效）。
func aiTokenPolicy(c config.AIConfig) aitoken.Policy {
	return aitoken.Policy{
		Prefix:         c.TokenPrefix,
		MinRole:        c.TokenMinRole,
		DefaultTTL:     parseDur(c.TokenDefaultTTL),
		MaxTTL:         parseDur(c.TokenMaxTTL),
		AllowPermanent: c.TokenAllowPermanent,
		MaxPerUser:     c.TokenMaxPerUser,
	}
}

// parseDur 解析时长；空值或非法值返回 0（配置在保存/加载时已校验）。
func parseDur(s string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// CreateAIToken 为当前用户创建 AI 访问令牌：POST /api/ai/tokens。明文只返回这一次。
func CreateAIToken(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		var in aitoken.CreateInput
		if err := httpx.DecodeJSON(r, &in); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		snap := s.Settings.Snapshot()
		created, err := s.AIToken.Create(r.Context(), u, s.Auth.Name(), in, aiTokenPolicy(snap.AI))
		if err != nil {
			auditOp(s, r, audit.ActionAITokenCreate, "ai_token", "", in.Name, err)
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		auditOpDetails(s, r, audit.ActionAITokenCreate, "ai_token", strconv.FormatInt(created.ID, 10),
			created.Name, nil, map[string]any{
				"allow_write": created.AllowWrite, "expires_at": created.ExpiresAt, "token_prefix": created.Prefix,
			}, nil)
		httpx.OK(w, created)
	}
}

// ListAITokens 列出可见令牌：GET /api/ai/tokens?mine=1&page=&size=。
// 普通用户只看本人；管理员看本租户；superadmin 看全部；mine=1 时只看本人。
func ListAITokens(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		snap := s.Settings.Snapshot()
		q := r.URL.Query()
		page := atoiDefault(q.Get("page"), 1)
		size := atoiDefault(q.Get("size"), snap.AI.ToolDefaultPageSize)
		if max := config.AIIntRanges["tool_default_page_size"][1]; size < 1 || size > max {
			size = snap.AI.ToolDefaultPageSize
		}
		mine := q.Get("mine") == "1" || q.Get("mine") == "true"
		list, total, err := s.AIToken.List(r.Context(), u, aitoken.ListOptions{Mine: mine, Page: page, Size: size})
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		pages := (total + size - 1) / size
		if pages < 1 {
			pages = 1
		}
		httpx.OK(w, map[string]any{"list": list, "total": total, "page": page, "size": size, "pages": pages})
	}
}

// RevokeAIToken 注销令牌：POST /api/ai/tokens/{id}/revoke。
func RevokeAIToken(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "令牌 ID 非法")
			return
		}
		t, err := s.AIToken.Revoke(r.Context(), u, id)
		if err != nil {
			auditOp(s, r, audit.ActionAITokenRevoke, "ai_token", idStr, "", err)
			httpx.FailErr(w, http.StatusNotFound, err)
			return
		}
		auditOp(s, r, audit.ActionAITokenRevoke, "ai_token", idStr, t.Name, nil)
		httpx.OK(w, map[string]any{"revoked": true, "id": id})
	}
}

// AIIntegration 返回 AI 接入信息：GET /api/ai/integration。
//
// 页面据此生成 OpenCode / Claude Code / Codex / stdio 桥的配置片段与 Skills 安装命令；
// 地址、服务名、令牌环境变量、安装目录全部来自设置，不在前端写死。
func AIIntegration(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := auth.FromContext(r.Context())
		snap := s.Settings.Snapshot()
		base := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
		dirs, _ := config.ParseSkillInstallDirs(snap.AI.SkillInstallDirs)
		if dirs == nil {
			dirs = []config.SkillInstallDir{}
		}
		httpx.OK(w, map[string]any{
			"base_url":                  base,
			"mcp_url":                   base + mcpPath,
			"mcp_path":                  mcpPath,
			"mcp_enabled":               snap.AI.MCPEnabled,
			"mcp_allow_write":           snap.AI.MCPAllowWrite,
			"mcp_require_confirm":       snap.AI.MCPRequireConfirm,
			"allow_command_tasks":       snap.AI.AllowCommandTasks,
			"server_name":               snap.AI.MCPServerName,
			"token_env":                 snap.AI.ClientTokenEnv,
			"token_prefix":              snap.AI.TokenPrefix,
			"token_default_ttl":         snap.AI.TokenDefaultTTL,
			"token_max_ttl":             snap.AI.TokenMaxTTL,
			"token_allow_permanent":     snap.AI.TokenAllowPermanent,
			"can_create_token":          aitoken.CanCreate(u, snap.AI.TokenMinRole),
			"token_min_role":            snap.AI.TokenMinRole,
			"assistant_enabled":         snap.AI.AssistantEnabled && assistantConfigured(snap.AI),
			"assistant_allow_write":     snap.AI.AssistantAllowWrite,
			"assistant_max_input_chars": snap.AI.AssistantMaxInputChars,
			"skill_install_dirs":        dirs,
			"skills":                    skillCatalog(snap, base),
			"protocol_versions":         mcpProtocolVersions(),
			"snippet_meta":              snippetMeta(),
			"bridge_availability":       bridgeAvailability(s),
		})
	}
}

// BatchDeleteAITokens 批量注销 AI 令牌：POST /api/ai/tokens/batch-delete。
func BatchDeleteAITokens(s *svc.ServiceContext) http.HandlerFunc {
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
		if len(req.IDs) > 200 {
			httpx.Fail(w, http.StatusBadRequest, "单次最多删除 200 个令牌")
			return
		}

		deleted := []int64{}
		failed := []map[string]any{}
		for _, id := range req.IDs {
			if _, err := s.AIToken.Delete(r.Context(), u, id); err != nil {
				failed = append(failed, map[string]any{"id": id, "error": err.Error()})
			} else {
				deleted = append(deleted, id)
			}
		}

		if len(deleted) > 0 {
			auditOpDetails(s, r, audit.ActionAITokenDelete, "ai_token_batch_delete",
				"", "", nil, map[string]any{"deleted": deleted, "failed_count": len(failed)}, nil)
		}
		httpx.OK(w, map[string]any{"deleted": deleted, "failed": failed})
	}
}
