package handler

import (
	"net/http"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
	"github.com/iflyelf/upgrade_mgr/skills"
)

// ListSkills 列出可用 Skills：GET /api/ai/skills。
func ListSkills(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := s.Settings.Snapshot()
		base := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
		httpx.OK(w, map[string]any{"skills": skillCatalog(snap, base)})
	}
}

// DownloadSkill 下载 Skill 归档：GET /api/ai/skills/{name}/archive。
//
// 接受 session cookie 或 Bearer AI token（curl 安装用）。
func DownloadSkill(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !skills.Exists(name) {
			http.NotFound(w, r)
			return
		}
		snap := s.Settings.Snapshot()
		base := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
		data, err := skills.Archive(name, skillData(snap, base), time.Now())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.tar.gz"`)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}
}

// DownloadSkillWithAuth 下载 Skill（支持 session 或 AI token）。
func DownloadSkillWithAuth(s *svc.ServiceContext) http.HandlerFunc {
	return requireSessionOrAIToken(s, DownloadSkill(s))
}

// requireSessionOrAIToken 中间件：接受会话 cookie 或 AI Bearer token。
func requireSessionOrAIToken(s *svc.ServiceContext, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, err := s.Auth.Authenticate(r); err == nil && u != nil {
			next(w, r.WithContext(auth.WithUser(r.Context(), u)))
			return
		}
		// 尝试 Bearer token（curl 安装用）。
		token := extractBearer(r.Header.Get("Authorization"))
		if token == "" {
			httpx.Fail(w, http.StatusUnauthorized, "需要登录或提供 AI 访问令牌")
			return
		}
		u, t, err := s.AIToken.Authenticate(r.Context(), token)
		if err != nil {
			httpx.Fail(w, http.StatusUnauthorized, "令牌无效或已过期")
			return
		}
		snap := s.Settings.Snapshot()
		interval := parseDur(snap.AI.TokenTouchInterval)
		if interval > 0 {
			s.AIToken.Touch(t.ID, clientIP(s, r), interval)
		}
		r = r.WithContext(auth.WithUser(r.Context(), u))
		next(w, r)
	}
}
