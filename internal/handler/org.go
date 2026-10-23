package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/logic/org"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// orgService 返回人员组织服务（惰性创建，避免 svc 增加字段）。
func orgService(s *svc.ServiceContext) *org.Service { return org.NewService(s.DB) }

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

// ---- 角色 ----

// RoleList 列出角色。
func RoleList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := orgService(s).ListRoles(r.Context())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}

// RoleCreate 创建角色。
func RoleCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name        string   `json:"name"`
			Code        string   `json:"code"`
			Description string   `json:"description"`
			Permissions []string `json:"permissions"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		id, err := orgService(s).CreateRole(r.Context(), req.Name, req.Code, req.Description, req.Permissions)
		auditOp(s, r, audit.ActionRoleCreate, "role", req.Name, req.Name, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// RoleUpdate 更新角色。
func RoleUpdate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "角色 ID 无效")
			return
		}
		var req struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Permissions []string `json:"permissions"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		err := orgService(s).UpdateRole(r.Context(), id, req.Name, req.Description, req.Permissions)
		auditOp(s, r, audit.ActionRoleUpdate, "role", strconv.FormatInt(id, 10), req.Name, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// RoleDelete 删除角色（被团队引用时拒绝）。
func RoleDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "角色 ID 无效")
			return
		}
		err := orgService(s).DeleteRole(r.Context(), id)
		auditOp(s, r, audit.ActionRoleDelete, "role", strconv.FormatInt(id, 10), "", err)
		if err != nil {
			httpx.Fail(w, http.StatusConflict, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// ---- 团队 ----

// TeamList 列出团队。
func TeamList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := orgService(s).ListTeams(r.Context())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}

// TeamCreate 创建团队（可同时指定成员）。
func TeamCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name        string   `json:"name"`
			Code        string   `json:"code"`
			Description string   `json:"description"`
			Members     []string `json:"members"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		creator := ""
		if u := actorOf(r); u != nil {
			creator = u.Username
		}
		id, err := orgService(s).CreateTeam(r.Context(), req.Name, req.Code, req.Description, req.Members, creator)
		auditOp(s, r, audit.ActionTeamCreate, "team", req.Name, req.Name, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// TeamUpdate 更新团队。
func TeamUpdate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "团队 ID 无效")
			return
		}
		var req struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Status      int    `json:"status"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		err := orgService(s).UpdateTeam(r.Context(), id, req.Name, req.Description, req.Status)
		auditOp(s, r, audit.ActionTeamUpdate, "team", strconv.FormatInt(id, 10), req.Name, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// TeamDelete 删除团队。
func TeamDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "团队 ID 无效")
			return
		}
		err := orgService(s).DeleteTeam(r.Context(), id)
		auditOp(s, r, audit.ActionTeamDelete, "team", strconv.FormatInt(id, 10), "", err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// TeamMembers 列出团队成员。
func TeamMembers(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "团队 ID 无效")
			return
		}
		list, err := orgService(s).ListMembers(r.Context(), id)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}

// TeamMemberAdd 批量添加成员。
func TeamMemberAdd(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "团队 ID 无效")
			return
		}
		var req struct {
			Members []string `json:"members"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		err := orgService(s).AddMembers(r.Context(), id, req.Members)
		auditOp(s, r, audit.ActionTeamUpdate, "team", strconv.FormatInt(id, 10),
			"add members: "+strings.Join(req.Members, ","), err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// TeamMemberRemove 移除成员。
func TeamMemberRemove(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "团队 ID 无效")
			return
		}
		userID := strings.TrimSpace(r.PathValue("user"))
		if userID == "" {
			httpx.Fail(w, http.StatusBadRequest, "缺少用户名")
			return
		}
		err := orgService(s).RemoveMember(r.Context(), id, userID)
		auditOp(s, r, audit.ActionTeamUpdate, "team", strconv.FormatInt(id, 10),
			"remove member: "+userID, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// ---- 服务组 ----

// GroupList 列出服务组（含组件）。
func GroupList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := orgService(s).ListGroups(r.Context())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}

// GroupCreate 创建服务组。
func GroupCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name         string  `json:"name"`
			Code         string  `json:"code"`
			Description  string  `json:"description"`
			ComponentIDs []int64 `json:"component_ids"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		creator := ""
		if u := actorOf(r); u != nil {
			creator = u.Username
		}
		id, err := orgService(s).CreateGroup(r.Context(), req.Name, req.Code, req.Description,
			req.ComponentIDs, creator)
		auditOp(s, r, audit.ActionGroupCreate, "service_group", req.Name, req.Name, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// GroupUpdate 更新服务组。
func GroupUpdate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "服务组 ID 无效")
			return
		}
		var req struct {
			Name         string  `json:"name"`
			Description  string  `json:"description"`
			ComponentIDs []int64 `json:"component_ids"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		err := orgService(s).UpdateGroup(r.Context(), id, req.Name, req.Description, req.ComponentIDs)
		auditOp(s, r, audit.ActionGroupUpdate, "service_group", strconv.FormatInt(id, 10), req.Name, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// GroupDelete 删除服务组。
func GroupDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "服务组 ID 无效")
			return
		}
		err := orgService(s).DeleteGroup(r.Context(), id)
		auditOp(s, r, audit.ActionGroupDelete, "service_group", strconv.FormatInt(id, 10), "", err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// ---- 团队授权 ----

// TeamPermissions 列出团队授权。
func TeamPermissions(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "团队 ID 无效")
			return
		}
		list, err := orgService(s).ListGroupPermissions(r.Context(), id)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}

// TeamPermissionSet 设置团队对服务组的授权。
func TeamPermissionSet(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		teamID, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "团队 ID 无效")
			return
		}
		var req struct {
			GroupID     int64    `json:"group_id"`
			Permissions []string `json:"permissions"`
			RoleIDs     []int64  `json:"role_ids"`
			Services    []string `json:"services"`
			HostGroups  []string `json:"host_groups"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if req.GroupID <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "缺少 group_id")
			return
		}
		err := orgService(s).SetGroupPermission(r.Context(), teamID, req.GroupID,
			req.Permissions, req.RoleIDs, req.Services, req.HostGroups)
		auditOp(s, r, audit.ActionPermissionSet, "team_permission",
			strconv.FormatInt(teamID, 10)+"/"+strconv.FormatInt(req.GroupID, 10),
			strings.Join(req.Permissions, ","), err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// TeamPermissionDelete 解除授权。
func TeamPermissionDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		teamID, ok := pathID(r, "id")
		if !ok {
			httpx.Fail(w, http.StatusBadRequest, "团队 ID 无效")
			return
		}
		groupID, err := strconv.ParseInt(r.PathValue("group"), 10, 64)
		if err != nil || groupID <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "服务组 ID 无效")
			return
		}
		err = orgService(s).DeleteGroupPermission(r.Context(), teamID, groupID)
		auditOp(s, r, audit.ActionPermissionDelete, "team_permission",
			strconv.FormatInt(teamID, 10)+"/"+strconv.FormatInt(groupID, 10), "", err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}
