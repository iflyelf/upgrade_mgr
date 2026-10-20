// Package middleware HTTP 中间件。
package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

type ctxKey int

const agentIDKey ctxKey = 1

// RequireAgentAuth Agent 认证中间件：从 Bearer token 校验 agent 身份。
//
// 请求头格式：Authorization: Bearer <agent_id>:<token>
// 校验通过后将 agent_id 注入 context，handler 通过 GetAgentID(r) 取用。
func RequireAgentAuth(s *svc.ServiceContext) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				httpx.Fail(w, http.StatusUnauthorized, "缺少 Agent 凭证")
				return
			}
			parts := strings.SplitN(strings.TrimPrefix(auth, "Bearer "), ":", 2)
			if len(parts) != 2 {
				httpx.Fail(w, http.StatusUnauthorized, "凭证格式错误")
				return
			}
			agentID, token := parts[0], parts[1]
			if err := s.AgentGW.VerifyToken(r.Context(), agentID, token); err != nil {
				httpx.Fail(w, http.StatusUnauthorized, "Agent 认证失败")
				return
			}
			ctx := context.WithValue(r.Context(), agentIDKey, agentID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetAgentID 从 context 中提取已认证的 agent_id（需配合 RequireAgentAuth 使用）。
func GetAgentID(r *http.Request) string {
	if v := r.Context().Value(agentIDKey); v != nil {
		return v.(string)
	}
	return ""
}
