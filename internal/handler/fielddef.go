package handler

import (
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/logic/fielddef"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// ensureUserInTenant 校验用户名属于当前管理员可见的租户（superadmin 不限）。
//
// 用户扩展字段以 username 为键、表本身不带 tenant_id；若不校验，租户管理员
// 可通过猜用户名读写其他租户人员的字段值。
func ensureUserInTenant(s *svc.ServiceContext, r *http.Request, username string) error {
	actor := actorOf(r)
	if actor == nil {
		return sql.ErrNoRows
	}
	if actor.IsSuperAdmin() {
		return nil
	}
	var tenantID int64
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT COALESCE(tenant_id,1) FROM local_users WHERE username=$1`, username).Scan(&tenantID)
	if err != nil {
		return err
	}
	if tenantID != actor.TenantID {
		return sql.ErrNoRows
	}
	return nil
}

// fieldDefRepo 按 kind 构造仓储（user / host）。
func fieldDefRepo(s *svc.ServiceContext, kind string) (*fielddef.Repo, bool) {
	repo, err := fielddef.NewRepo(s.DB, kind)
	if err != nil {
		return nil, false
	}
	return repo, true
}

// ListFieldDefs 列出字段定义（管理员）。
func ListFieldDefs(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := strings.TrimSpace(r.PathValue("kind"))
		repo, ok := fieldDefRepo(s, kind)
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "不支持的字段类型（仅 user/host）")
			return
		}
		list, err := repo.List(r.Context())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}

// CreateFieldDef 新增字段定义（管理员）。
func CreateFieldDef(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := strings.TrimSpace(r.PathValue("kind"))
		repo, ok := fieldDefRepo(s, kind)
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "不支持的字段类型（仅 user/host）")
			return
		}
		var d fielddef.Def
		if err := httpx.DecodeJSON(r, &d); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		d.Builtin = false
		id, err := repo.Create(r.Context(), &d)
		auditOp(s, r, audit.ActionFieldDefCreate, kind+"_field", d.FieldKey, d.Label, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// UpdateFieldDef 更新字段定义（管理员）。
func UpdateFieldDef(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := strings.TrimSpace(r.PathValue("kind"))
		repo, ok := fieldDefRepo(s, kind)
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "不支持的字段类型（仅 user/host）")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "字段 ID 无效")
			return
		}
		var d fielddef.Def
		if err := httpx.DecodeJSON(r, &d); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		d.ID = id
		err = repo.Update(r.Context(), &d)
		auditOp(s, r, audit.ActionFieldDefUpdate, kind+"_field", strconv.FormatInt(id, 10), d.Label, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// DeleteFieldDef 删除字段定义（管理员；内置字段拒绝）。
func DeleteFieldDef(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := strings.TrimSpace(r.PathValue("kind"))
		repo, ok := fieldDefRepo(s, kind)
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "不支持的字段类型（仅 user/host）")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "字段 ID 无效")
			return
		}
		err = repo.Delete(r.Context(), id)
		auditOp(s, r, audit.ActionFieldDefDelete, kind+"_field", strconv.FormatInt(id, 10), "", err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// GetUserFields 读取某用户的扩展字段值（管理员）。
func GetUserFields(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := strings.TrimSpace(r.PathValue("username"))
		if username == "" {
			httpx.Fail(w, http.StatusBadRequest, "缺少用户名")
			return
		}
		if err := ensureUserInTenant(s, r, username); err != nil {
			httpx.Fail(w, http.StatusNotFound, "用户不存在或无权访问")
			return
		}
		repo, ok := fieldDefRepo(s, fielddef.KindUser)
		if !ok {
			httpx.Fail(w, http.StatusInternalServerError, "字段仓储不可用")
			return
		}
		defs, err := repo.List(r.Context())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		values, err := repo.UserValues(r.Context(), username)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{"defs": defs, "values": values})
	}
}

// SaveUserFields 保存某用户的扩展字段值（管理员）。
func SaveUserFields(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := strings.TrimSpace(r.PathValue("username"))
		if username == "" {
			httpx.Fail(w, http.StatusBadRequest, "缺少用户名")
			return
		}
		if err := ensureUserInTenant(s, r, username); err != nil {
			httpx.Fail(w, http.StatusNotFound, "用户不存在或无权访问")
			return
		}
		var req struct {
			Values map[string]string `json:"values"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		repo, ok := fieldDefRepo(s, fielddef.KindUser)
		if !ok {
			httpx.Fail(w, http.StatusInternalServerError, "字段仓储不可用")
			return
		}
		defs, err := repo.List(r.Context())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		// 只允许编辑 editable=true 的字段。
		editable := make([]fielddef.Def, 0, len(defs))
		for _, d := range defs {
			if d.Editable {
				editable = append(editable, d)
			}
		}
		clean, err := fielddef.ValidateValues(editable, req.Values)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		err = repo.SaveUserValues(r.Context(), username, clean)
		auditOp(s, r, audit.ActionFieldValueSave, "user_field_values", username,
			strings.Join(sortedKeys(clean), ","), err)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, nil)
	}
}

// sortedKeys 返回有序键（日志稳定）。
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ListUserFieldsWithValues 列出用户及其扩展字段值（管理员，用于「人员信息」表格）。
func ListUserFieldsWithValues(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := s.RBAC.List(r.Context(), actorOf(r))
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		repo, ok := fieldDefRepo(s, fielddef.KindUser)
		if !ok {
			httpx.Fail(w, http.StatusInternalServerError, "字段仓储不可用")
			return
		}
		defs, err := repo.List(r.Context())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		// 一次性读出全部字段值，避免按用户逐个查询（N+1）。
		// 只读取当前管理员可见的用户，防止多租户场景泄露其他租户的人员字段。
		usernames := make([]string, 0, len(users))
		for _, u := range users {
			usernames = append(usernames, u.Username)
		}
		all, err := repo.UserValuesForUsernames(r.Context(), usernames)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{"users": users, "defs": defs, "values": all})
	}
}
