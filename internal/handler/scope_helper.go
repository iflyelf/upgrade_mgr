package handler

import (
	"net/http"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/middleware"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// reqScope 从请求上下文推导租户作用域。
//
// 用户身份由 RequireAuth 中间件注入；若缺失（未认证路径误用本函数），
// scope.ForUser(nil) 会 fail-close 到空集，不会泄露任何租户数据。
func reqScope(r *http.Request) scope.Scope {
	u, ok := auth.FromContext(r.Context())
	if !ok {
		return scope.ForUser(nil)
	}
	return scope.ForUser(u)
}

// agentScope 从 Agent 认证上下文推导租户作用域。
//
// Agent 走 Bearer 认证（无 Web 会话），其可见范围由所属主机的 tenant_id 决定。
// 查不到归属时 fail-close 到空集，避免 Agent 越租户读取制品。
func agentScope(s *svc.ServiceContext, r *http.Request) scope.Scope {
	agentID := middleware.GetAgentID(r)
	if agentID == "" {
		return scope.ForUser(nil)
	}
	var tenantID int64
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT COALESCE(tenant_id, 1) FROM hosts WHERE agent_id=$1`, agentID).Scan(&tenantID)
	if err != nil || tenantID <= 0 {
		return scope.ForUser(nil)
	}
	return scope.New(tenantID, false)
}
