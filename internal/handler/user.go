// 用户与租户管理 handlers（管理员专用）。
//
// 多租户若没有「创建其他租户用户」的入口，租户隔离等于不可用，
// 因此这些接口是多租户能力的必要组成部分，而非可选增强。
package handler

import (
	"net/http"
	"strconv"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/rbac"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

func actorOf(r *http.Request) *auth.User {
	u, ok := auth.FromContext(r.Context())
	if !ok {
		return nil
	}
	return u
}

// UserList 列出用户（非 superadmin 仅本租户）。
func UserList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := s.RBAC.List(r.Context(), actorOf(r))
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}

// UserCreate 创建用户。
//
// 密码留空时生成一次性随机密码并在响应中返回（仅本次可见），
// 避免为了"快速加人"设置弱口令或默认密码。
func UserCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req rbac.CreateRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		initialPassword := ""
		if req.Password == "" {
			initialPassword, _ = generatePassword(16)
			req.Password = initialPassword
		}
		id, err := s.RBAC.Create(r.Context(), actorOf(r), req)
		auditOp(s, r, audit.ActionUserCreate, "user", req.Username, req.Username, err)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		resp := map[string]any{"id": id}
		if initialPassword != "" {
			resp["initial_password"] = initialPassword
		}
		httpx.OK(w, resp)
	}
}

// UserSetTenantRoles 调整用户租户与角色。
func UserSetTenantRoles(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.PathValue("username")
		var req struct {
			TenantID int64    `json:"tenant_id"`
			Roles    []string `json:"roles"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		err := s.RBAC.SetTenantRoles(r.Context(), actorOf(r), username, req.TenantID, req.Roles)
		auditOp(s, r, audit.ActionUserUpdate, "user", username, username, err)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		httpx.OK(w, nil)
	}
}

// UserSetDisabled 启用/禁用用户。
func UserSetDisabled(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.PathValue("username")
		var req struct {
			Disabled bool `json:"disabled"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		err := s.RBAC.SetDisabled(r.Context(), actorOf(r), username, req.Disabled)
		auditOp(s, r, audit.ActionUserUpdate, "user", username, username, err)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		httpx.OK(w, nil)
	}
}

// TenantList 列出租户（仅 superadmin 可见全部，其他人只看自己所属）。
func TenantList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := actorOf(r)
		q := `SELECT id, name, COALESCE(display_name,''), COALESCE(status,'active') FROM tenants`
		var args []any
		if actor == nil || !actor.IsSuperAdmin() {
			tid := int64(-1)
			if actor != nil {
				tid = actor.TenantID
			}
			q += ` WHERE id=$1`
			args = append(args, tid)
		}
		q += ` ORDER BY id`
		rows, err := s.DB.QueryContext(r.Context(), q, args...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var id int64
			var name, display, status string
			if err := rows.Scan(&id, &name, &display, &status); err != nil {
				continue
			}
			list = append(list, map[string]any{
				"id": id, "name": name, "display_name": display, "status": status,
			})
		}
		httpx.OK(w, list)
	}
}

// TenantCreate 创建租户（仅 superadmin）。
func TenantCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := actorOf(r)
		if actor == nil || !actor.IsSuperAdmin() {
			httpx.Fail(w, http.StatusForbidden, "仅 superadmin 可创建租户")
			return
		}
		var req struct {
			Name    string `json:"name"`
			Display string `json:"display_name"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil || req.Name == "" {
			httpx.Fail(w, http.StatusBadRequest, "name 必填")
			return
		}
		var id int64
		err := s.DB.QueryRowContext(r.Context(),
			`INSERT INTO tenants (name, display_name, status) VALUES ($1,$2,'active') RETURNING id`,
			req.Name, req.Display).Scan(&id)
		auditOp(s, r, audit.ActionTenantCreate, "tenant", strconv.FormatInt(id, 10), req.Name, err)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// UserDelete 删除用户（管理员）。
func UserDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.PathValue("username")
		err := s.RBAC.Delete(r.Context(), actorOf(r), username)
		auditOp(s, r, audit.ActionUserDelete, "user", username, username, err)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		httpx.OK(w, nil)
	}
}

// UserResetPassword 管理员重置用户密码。
func UserResetPassword(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.PathValue("username")
		var req struct {
			Password string `json:"password"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		err := s.RBAC.ResetPassword(r.Context(), actorOf(r), username, req.Password)
		auditOp(s, r, audit.ActionUserUpdate, "user", username, "reset-password", err)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		httpx.OK(w, nil)
	}
}
