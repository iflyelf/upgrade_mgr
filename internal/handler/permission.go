package handler

import (
	"net/http"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// MyPermissions 返回当前用户的有效权限，供前端过滤可操作项（组件/主机分组）。
//
// 管理员返回 admin=true（表示不受组件级限制）；普通用户返回
// components[name][perm] 与 host_groups[name][] 结构，与团队授权模型一致。
// 只读接口，写操作仍以服务端逐次校验为准。
func MyPermissions(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证，请先登录")
			return
		}
		if actorIsPrivileged(u) {
			httpx.OK(w, map[string]any{
				"admin": true, "username": u.Username,
				"components": map[string]any{}, "host_groups": map[string]any{},
				"owner_mode": "off",
			})
			return
		}
		perms, hosts, err := orgService(s).EffectivePermissionsWithHosts(r.Context(), u.Username)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{
			"admin": false, "username": u.Username,
			"components": perms, "host_groups": hosts,
			"owner_mode": s.Task.AccessMode(r.Context(), u.Username),
		})
	}
}
