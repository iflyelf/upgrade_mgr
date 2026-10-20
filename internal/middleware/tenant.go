package middleware

import (
	"net/http"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/tenantscope"
)

// WithTenantScope 依据「已认证用户」推导租户作用域并注入 context。
//
// 安全要点：
//   - 作用域完全来自会话中的用户身份，绝不读取请求参数里的 tenant_id，
//     否则调用方可通过传参跨租户读写；
//   - 仅 superadmin 角色可跨租户；
//   - 必须置于 RequireAuth 之后使用。
func WithTenantScope(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证，请先登录")
			return
		}
		scope := tenantscope.Scope{
			TenantID: u.TenantID,
			All:      u.HasRole(auth.RoleSuperAdmin),
		}
		if scope.TenantID <= 0 {
			scope.TenantID = 1
		}
		next(w, r.WithContext(tenantscope.With(r.Context(), scope)))
	}
}
