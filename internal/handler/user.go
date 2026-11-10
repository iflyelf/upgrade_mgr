// 用户与租户管理 handlers（管理员专用）。
//
// 多租户若没有「创建其他租户用户」的入口，租户隔离等于不可用，
// 因此这些接口是多租户能力的必要组成部分，而非可选增强。
package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/casdoor"
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

// casdoorDirectory 按当前配置构造 Casdoor 目录客户端。
//
// Casdoor 连接配置属于「需重启」项，但仍通过设置快照读取，避免与设置页并发写冲突。
func casdoorDirectory(s *svc.ServiceContext) *casdoor.Directory {
	cfg := s.Config.Casdoor
	if s.Settings != nil {
		snap := s.Settings.Snapshot()
		cfg = snap.Casdoor
	}
	return casdoor.NewDirectory(cfg.Endpoint, cfg.ClientId, cfg.ClientSecret, cfg.OrganizationName, cfg.ApplicationName)
}

// UserDirectory 返回 Casdoor 用户目录（服务端分页 + 搜索），并合并本地租户/角色。
//
// 与 /api/users（本地授权表）的区别：这是「人员目录」，用于用户管理页展示
// Casdoor 中的真实用户；local=true 表示已在本系统建档（可分配租户/角色）。
// 直接使用 Casdoor 服务端分页，避免 FlyIAM 全量导出 3 万用户时的超时。
func UserDirectory(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dir := casdoorDirectory(s)
		if !dir.Valid() {
			// 纯内置账号部署（未配置 Casdoor）：退回本地用户表，保持页面可用。
			list, lerr := s.RBAC.List(r.Context(), actorOf(r))
			if lerr != nil {
				httpx.FailErr(w, http.StatusInternalServerError, lerr)
				return
			}
			out := make([]map[string]any, 0, len(list))
			for _, u := range list {
				out = append(out, map[string]any{
					"username": u.Username, "name": u.Username, "display_name": u.Display,
					"local": true, "tenant_id": u.TenantID, "roles": u.Roles,
					"is_admin": u.IsAdmin, "disabled": u.Disabled,
					"casdoor": false, "casdoor_admin": false, "is_forbidden": false,
					"properties": map[string]string{},
				})
			}
			httpx.OK(w, map[string]any{"list": out, "total": len(out), "page": 1, "size": len(out)})
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		if page < 1 {
			page = 1
		}
		if size < 1 {
			size = 20
		}
		if size > 200 {
			size = 200
		}
		field := strings.TrimSpace(r.URL.Query().Get("field"))
		keyword := strings.TrimSpace(r.URL.Query().Get("keyword"))

		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		users, total, err := dir.List(ctx, page, size, field, keyword)
		if err != nil {
			httpx.Fail(w, http.StatusBadGateway, "读取 Casdoor 用户失败: "+err.Error())
			return
		}

		// 一次 SQL 批量取本页用户的本地授权信息，避免 N+1 查询。
		names := make([]string, 0, len(users))
		for _, u := range users {
			names = append(names, u.Name)
		}
		type localRow struct {
			display  string
			tenant   int64
			roles    pq.StringArray
			isAdmin  bool
			disabled bool
		}
		locals := make(map[string]localRow, len(names))
		if len(names) > 0 {
			rows, qerr := s.DB.QueryContext(ctx, `
				SELECT username, COALESCE(display_name,''), COALESCE(tenant_id,1),
				       COALESCE(roles, ARRAY['user']::text[]), is_admin, disabled
				FROM local_users WHERE username = ANY($1)`, pq.Array(names))
			if qerr != nil {
				httpx.FailErr(w, http.StatusInternalServerError, qerr)
				return
			}
			for rows.Next() {
				var name string
				var lr localRow
				if scanErr := rows.Scan(&name, &lr.display, &lr.tenant, &lr.roles,
					&lr.isAdmin, &lr.disabled); scanErr != nil {
					rows.Close()
					httpx.FailErr(w, http.StatusInternalServerError, scanErr)
					return
				}
				locals[name] = lr
			}
			rows.Close()
			if rowsErr := rows.Err(); rowsErr != nil {
				httpx.FailErr(w, http.StatusInternalServerError, rowsErr)
				return
			}
		}

		out := make([]map[string]any, 0, len(users))
		for _, u := range users {
			display := u.DisplayName
			row := map[string]any{
				"username": u.Name, "name": u.Name, "display_name": display,
				"email": u.Email, "phone": u.Phone, "avatar": u.Avatar, "owner": u.Owner,
				"casdoor_admin": u.IsAdmin, "is_forbidden": u.IsForbidden,
				"properties": u.Properties, "created_time": u.CreatedTime,
				// 未分配过本地授权的用户按默认 user/租户 1 展示；
				// 编辑角色/禁用时后端会隐式建档，无需单独的「开通」步骤。
				"casdoor": true, "local": false, "tenant_id": 1, "roles": []string{"user"},
				"is_admin": false, "disabled": false,
			}
			if lr, ok := locals[u.Name]; ok {
				row["local"] = true
				row["tenant_id"] = lr.tenant
				row["roles"] = []string(lr.roles)
				row["is_admin"] = lr.isAdmin
				row["disabled"] = lr.disabled
				// 显示名以 Casdoor 为准；仅当 Casdoor 为空时回退本地旧值。
				if strings.TrimSpace(display) == "" && strings.TrimSpace(lr.display) != "" {
					row["display_name"] = lr.display
				}
			}
			out = append(out, row)
		}
		httpx.OK(w, map[string]any{"list": out, "total": total, "page": page, "size": size})
	}
}

// UserCasdoorCreate 在 Casdoor 创建用户，并同步开通本地授权。
//
// 扩展字段（properties）不在本接口写入：人事属性只从 FlyIAM 同步，
// 避免两个系统互相覆盖。
func UserCasdoorCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"display_name"`
			Email       string   `json:"email"`
			Phone       string   `json:"phone"`
			Avatar      string   `json:"avatar"`
			Password    string   `json:"password"`
			Roles       []string `json:"roles"`
			TenantID    int64    `json:"tenant_id"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" || len(req.Name) > 100 {
			httpx.Fail(w, http.StatusBadRequest, "域账号必填且不超过 100 字符")
			return
		}
		dir := casdoorDirectory(s)
		if !dir.Valid() {
			httpx.Fail(w, http.StatusServiceUnavailable, "Casdoor 未配置，无法创建用户")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		existing, err := dir.Get(ctx, req.Name)
		if err != nil {
			httpx.Fail(w, http.StatusBadGateway, "校验 Casdoor 用户失败: "+err.Error())
			return
		}
		if existing != nil {
			httpx.Fail(w, http.StatusConflict, "Casdoor 已存在用户 "+req.Name)
			return
		}

		generated := false
		if strings.TrimSpace(req.Password) == "" {
			req.Password, _ = generatePassword(16)
			generated = true
		}
		if len(req.Password) < 8 {
			httpx.Fail(w, http.StatusBadRequest, "密码长度不能少于 8 个字符")
			return
		}

		if err := dir.CreateUser(ctx, casdoor.CreateUserInput{
			Name: req.Name, DisplayName: req.DisplayName, Email: req.Email,
			Phone: req.Phone, Avatar: req.Avatar, Password: req.Password,
		}); err != nil {
			auditOp(s, r, audit.ActionUserCreate, "user", req.Name, req.Name, err)
			httpx.Fail(w, http.StatusBadGateway, "创建 Casdoor 用户失败: "+err.Error())
			return
		}

		// 同时开通本地授权（角色默认 user），避免创建后还要再点一次「开通」。
		id, localErr := s.RBAC.EnableSSOUser(ctx, actorOf(r), req.Name, req.DisplayName, req.TenantID, req.Roles)
		auditOp(s, r, audit.ActionUserCreate, "user", req.Name, req.Name, localErr)
		resp := map[string]any{"id": id, "username": req.Name}
		if generated {
			resp["initial_password"] = req.Password
		}
		if localErr != nil {
			resp["warning"] = "Casdoor 用户已创建，但本地授权失败: " + localErr.Error()
		}
		httpx.OK(w, resp)
	}
}

// UserCasdoorUpdate 更新 Casdoor 用户基础资料（不含 properties）。
func UserCasdoorUpdate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := strings.TrimSpace(r.PathValue("username"))
		if username == "" {
			httpx.Fail(w, http.StatusBadRequest, "用户名为空")
			return
		}
		var req struct {
			DisplayName string `json:"display_name"`
			Email       string `json:"email"`
			Phone       string `json:"phone"`
			Avatar      string `json:"avatar"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		dir := casdoorDirectory(s)
		if !dir.Valid() {
			httpx.Fail(w, http.StatusServiceUnavailable, "Casdoor 未配置，无法更新用户")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		err := dir.UpdateProfile(ctx, username, req.DisplayName, req.Email, req.Phone, req.Avatar)
		auditOp(s, r, audit.ActionUserUpdate, "user", username, username, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadGateway, "更新 Casdoor 用户失败: "+err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// UserCasdoorResetPassword 重置 Casdoor 用户密码（留空则生成一次性随机密码）。
func UserCasdoorResetPassword(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := strings.TrimSpace(r.PathValue("username"))
		if username == "" {
			httpx.Fail(w, http.StatusBadRequest, "用户名为空")
			return
		}
		var req struct {
			Password string `json:"password"`
		}
		if r.ContentLength != 0 {
			if err := httpx.DecodeJSON(r, &req); err != nil {
				httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
				return
			}
		}
		generated := false
		if strings.TrimSpace(req.Password) == "" {
			req.Password, _ = generatePassword(16)
			generated = true
		}
		if len(req.Password) < 8 {
			httpx.Fail(w, http.StatusBadRequest, "密码长度不能少于 8 个字符")
			return
		}
		dir := casdoorDirectory(s)
		if !dir.Valid() {
			httpx.Fail(w, http.StatusServiceUnavailable, "Casdoor 未配置，无法重置密码")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		err := dir.SetPassword(ctx, username, req.Password)
		auditOp(s, r, audit.ActionPasswordChange, "user", username, username, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadGateway, "重置 Casdoor 密码失败: "+err.Error())
			return
		}
		resp := map[string]any{"username": username}
		if generated {
			resp["password"] = req.Password
		}
		httpx.OK(w, resp)
	}
}

// UserCasdoorSetAdmin 设置/取消 Casdoor 管理员标记（FlyIAM 全局管理员）。
func UserCasdoorSetAdmin(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := strings.TrimSpace(r.PathValue("username"))
		if username == "" {
			httpx.Fail(w, http.StatusBadRequest, "用户名为空")
			return
		}
		var req struct {
			IsAdmin bool `json:"is_admin"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		dir := casdoorDirectory(s)
		if !dir.Valid() {
			httpx.Fail(w, http.StatusServiceUnavailable, "Casdoor 未配置，无法修改管理员标记")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		err := dir.SetAdmin(ctx, username, req.IsAdmin)
		auditOp(s, r, audit.ActionUserUpdate, "user", username, username, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadGateway, "修改 Casdoor 管理员标记失败: "+err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}
