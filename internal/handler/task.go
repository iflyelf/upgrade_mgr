package handler

import (
	"net/http"
	"strconv"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/task"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TaskPreview 预览命中主机与分批。
func TaskPreview(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req task.CreateRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		res, err := s.Task.Preview(r.Context(), reqScope(r), req)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		httpx.OK(w, res)
	}
}

// TaskCreate 创建任务。
func TaskCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req task.CreateRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if u, ok := auth.FromContext(r.Context()); ok {
			req.CreatedBy = u.Username
		}
		id, err := s.Task.Create(r.Context(), reqScope(r), req)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// TaskList 任务列表。
func TaskList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 租户隔离：只列出当前租户的任务（superadmin 跨租户）
		tCond, tArgs := reqScope(r).Where("t", 1)
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT t.id, t.name, t.component_id, t.version, t.status, t.created_at,
			       COUNT(th.id) AS total,
			       COUNT(th.id) FILTER (WHERE th.status='success') AS success,
			       COUNT(th.id) FILTER (WHERE th.status='failed') AS failed
			FROM upgrade_tasks t LEFT JOIN task_hosts th ON th.task_id=t.id
			WHERE `+tCond+`
			GROUP BY t.id ORDER BY t.created_at DESC LIMIT 200`, tArgs...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var id, cid int64
			var name, version, status string
			var createdAt any
			var total, success, failed int
			if err := rows.Scan(&id, &name, &cid, &version, &status, &createdAt, &total, &success, &failed); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			list = append(list, map[string]any{
				"id": id, "name": name, "component_id": cid, "version": version,
				"status": status, "created_at": createdAt,
				"total": total, "success": success, "failed": failed,
			})
		}
		httpx.OK(w, list)
	}
}

// TaskDetail 任务详情（含逐机状态）。
func TaskDetail(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var name, version, status string
		// 租户隔离：非本租户任务一律视为不存在（不泄露存在性）
		dCond, dArgs := reqScope(r).Where("", 2)
		err := s.DB.QueryRowContext(r.Context(),
			`SELECT name, version, status FROM upgrade_tasks WHERE id=$1 AND `+dCond,
			append([]any{id}, dArgs...)...).Scan(&name, &version, &status)
		if err != nil {
			httpx.Fail(w, http.StatusNotFound, "任务不存在")
			return
		}
		rows, _ := s.DB.QueryContext(r.Context(), `
			SELECT h.ip, th.batch_index, th.status, th.stage, th.from_version, th.to_version, th.exit_code
			FROM task_hosts th JOIN hosts h ON h.id=th.host_id
			WHERE th.task_id=$1 ORDER BY th.batch_index, h.ip`, id)
		defer rows.Close()
		hosts := []map[string]any{}
		for rows.Next() {
			var ip, st, stage, fromV, toV string
			var batch, exit int
			_ = rows.Scan(&ip, &batch, &st, &stage, &fromV, &toV, &exit)
			hosts = append(hosts, map[string]any{
				"ip": ip, "batch": batch, "status": st, "stage": stage,
				"from_version": fromV, "to_version": toV, "exit_code": exit,
			})
		}
		httpx.OK(w, map[string]any{
			"id": id, "name": name, "version": version, "status": status, "hosts": hosts,
		})
	}
}

// TaskStart / TaskPause / TaskCancel。
func TaskStart(s *svc.ServiceContext) http.HandlerFunc {
	return taskAction(s, audit.ActionTaskStart, func(id int64, r *http.Request) error { return s.Task.Start(r.Context(), reqScope(r), id) })
}
func TaskPause(s *svc.ServiceContext) http.HandlerFunc {
	return taskAction(s, audit.ActionTaskPause, func(id int64, r *http.Request) error { return s.Task.Pause(r.Context(), reqScope(r), id) })
}
func TaskCancel(s *svc.ServiceContext) http.HandlerFunc {
	return taskAction(s, audit.ActionTaskCancel, func(id int64, r *http.Request) error { return s.Task.Cancel(r.Context(), reqScope(r), id) })
}
func TaskPromote(s *svc.ServiceContext) http.HandlerFunc {
	return taskAction(s, audit.ActionTaskPromote, func(id int64, r *http.Request) error { return s.Task.Promote(r.Context(), reqScope(r), id) })
}

// taskAction 统一的任务操作入口：执行 + 审计（成功与失败均留痕）。
func taskAction(s *svc.ServiceContext, action string, fn func(int64, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		id, _ := strconv.ParseInt(idStr, 10, 64)
		if err := fn(id, r); err != nil {
			auditOp(s, r, action, "task", idStr, "", err)
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		auditOp(s, r, action, "task", idStr, "", nil)
		httpx.OK(w, nil)
	}
}
