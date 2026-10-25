package handler

import (
	"log"
	"net/http"
	"strconv"

	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// ComponentList 列出全部组件。
func ComponentList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := s.Component.List(r.Context(), reqScope(r))
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}

// ComponentGet 获取单个组件。
func ComponentGet(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		c, err := s.Component.Get(r.Context(), reqScope(r), id)
		if err != nil {
			httpx.FailErr(w, http.StatusNotFound, err)
			return
		}
		httpx.OK(w, c)
	}
}

// ComponentCreate 新建组件。
func ComponentCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var c component.Component
		if err := httpx.DecodeJSON(r, &c); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if c.Name == "" {
			httpx.Fail(w, http.StatusBadRequest, "name 必填")
			return
		}
		if err := component.ValidateScriptURL(c.ScriptURL); err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		id, err := s.Component.Create(r.Context(), reqScope(r), &c)
		auditOp(s, r, audit.ActionComponentCreate, "component", strconv.FormatInt(id, 10), c.Name, err)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// ComponentUpdate 更新组件。
func ComponentUpdate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var c component.Component
		if err := httpx.DecodeJSON(r, &c); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		c.ID = id
		if err := component.ValidateScriptURL(c.ScriptURL); err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		err := s.Component.Update(r.Context(), reqScope(r), &c)
		auditOp(s, r, audit.ActionComponentUpdate, "component", strconv.FormatInt(id, 10), c.Name, err)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, nil)
	}
}

// ComponentDelete 删除组件。
//
// 默认拒绝删除仍被任务引用的组件；查询参数 cascade_tasks=true 时连带删除引用任务
// （运行中/暂停中的任务仍拒绝）。预检先于制品文件清理，避免文件已删但组件删除失败。
func ComponentDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		cascade := r.URL.Query().Get("cascade_tasks") == "true" || r.URL.Query().Get("cascade_tasks") == "1"
		sc := reqScope(r)
		plan, err := s.Component.CheckDelete(r.Context(), sc, id, cascade)
		if err != nil {
			auditOp(s, r, audit.ActionComponentDelete, "component", strconv.FormatInt(id, 10), "", err)
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		// 与制品上传共用组件级 advisory lock：删除期间阻止新上传，
		// 并等待正在上传的制品完成，避免删除文件/元数据后又被写入。
		lock, lerr := s.Artifact.LockComponent(r.Context(), id)
		if lerr != nil {
			auditOp(s, r, audit.ActionComponentDelete, "component", strconv.FormatInt(id, 10), "", lerr)
			httpx.FailErrf(w, http.StatusConflict, "组件正在上传或删除制品，请稍后重试", lerr)
			return
		}
		defer lock.Unlock()
		// 先取制品路径，删除成功后再删文件：避免组件删不掉（如仍被运行中任务引用）
		// 但制品文件已被清空。元数据随 components 的 CASCADE 一并删除。
		paths, perr := s.Artifact.PathsByComponent(r.Context(), sc, id)
		if perr != nil {
			log.Printf("⚠️ 查询组件 %d 的制品文件失败: %v", id, perr)
		}
		var deletedTasks int64
		if cascade {
			deletedTasks, err = s.Component.DeleteWithTasks(r.Context(), sc, id)
		} else {
			err = s.Component.Delete(r.Context(), sc, id)
		}
		auditOpDetails(s, r, audit.ActionComponentDelete, "component", strconv.FormatInt(id, 10), "",
			err, map[string]any{"cascade_tasks": cascade, "deleted_tasks": deletedTasks},
			map[string]any{"referenced_tasks": plan.TaskCount})
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		s.Artifact.RemoveObjects(paths)
		httpx.OK(w, map[string]any{"deleted_tasks": deletedTasks})
	}
}

// ComponentReferences 返回引用该组件的任务数量与列表，供删除前确认。
func ComponentReferences(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		sc := reqScope(r)
		plan, err := s.Component.CheckDelete(r.Context(), sc, id, true)
		// 存在引用时 CheckDelete 会返回错误，但引用查询本身应成功返回明细。
		if err != nil && plan.TaskCount == 0 {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		tCond, tArgs := sc.Where("", 2)
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT id, COALESCE(kind,'upgrade'), name, status, created_at
			FROM upgrade_tasks WHERE component_id=$1 AND `+tCond+`
			ORDER BY created_at DESC LIMIT 200`, append([]any{id}, tArgs...)...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var tid int64
			var kind, name, status string
			var createdAt any
			if err := rows.Scan(&tid, &kind, &name, &status, &createdAt); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			list = append(list, map[string]any{
				"id": tid, "kind": kind, "name": name, "status": status, "created_at": createdAt,
			})
		}
		httpx.OK(w, map[string]any{
			"task_count": plan.TaskCount, "blocking_tasks": plan.BlockingTasks,
			"active_commands": plan.ActiveCommands, "tasks": list,
		})
	}
}
