package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/lib/pq"

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
		if u, ok := auth.FromContext(r.Context()); ok {
			req.SetActor(u)
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
			req.SetActor(u)
		}
		id, err := s.Task.Create(r.Context(), reqScope(r), req)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// actorIsPrivileged 判断当前用户是否管理员；nil（内部调用）视为特权。
func actorIsPrivileged(u *auth.User) bool {
	return u == nil || u.IsAdmin || u.IsSuperAdmin() || u.HasRole(auth.RoleAdmin)
}

// taskListFilter 构造任务列表的筛选条件（租户 + kind/component_id/status/q/created_by），
// 列表与「按筛选条件全选」共用，保证两者命中的任务集合一致。
// 表别名固定为 t；返回的占位符从 $1 起连续编号。
func taskListFilter(r *http.Request, actor *auth.User) ([]string, []any) {
	tCond, tArgs := reqScope(r).Where("t", 1)
	where := []string{tCond}
	args := append([]any{}, tArgs...)
	add := func(expr string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	q := r.URL.Query()
	if kind := q.Get("kind"); kind != "" {
		add("COALESCE(t.kind,'upgrade')=$%d", kind)
	}
	if cidStr := q.Get("component_id"); cidStr != "" {
		if cid, err := strconv.ParseInt(cidStr, 10, 64); err == nil {
			add("t.component_id=$%d", cid)
		}
	}
	if status := q.Get("status"); status != "" {
		add("t.status=$%d", status)
	}
	if !actorIsPrivileged(actor) {
		where = append(where, "COALESCE(t.kind,'upgrade') <> 'command'")
	}
	if kw := strings.TrimSpace(q.Get("q")); kw != "" {
		add("t.name ILIKE $%d", "%"+kw+"%")
	}
	if creator := strings.TrimSpace(q.Get("created_by")); creator != "" {
		add("COALESCE(t.created_by,'')=$%d", creator)
	}
	return where, args
}

// TaskDeletableIDs 返回当前筛选条件下可删除任务的 ID（草稿/终态），供「按筛选条件全选」。
//
// 与列表使用同一筛选；「仅本人」策略下非管理员只返回自己创建的任务。
// 最多返回 task.MaxBatchDelete 个，truncated=true 表示还有更多。
// 这里只是候选集合，真正删除时 BatchDelete 仍会逐个复核权限、状态与未完成指令。
func TaskDeletableIDs(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, _ := auth.FromContext(r.Context())
		where, args := taskListFilter(r, actor)
		where = append(where, "t.status IN ('draft','success','failed','canceled','rolled_back')")
		if !actorIsPrivileged(actor) && actor != nil &&
			s.Task.AccessMode(r.Context(), actor.Username) == "on" {
			args = append(args, actor.Username)
			where = append(where, fmt.Sprintf("(COALESCE(t.created_by,'')='' OR t.created_by=$%d)", len(args)))
		}
		// 多取一条用于判断是否被截断。
		args = append(args, task.MaxBatchDelete+1)
		rows, err := s.DB.QueryContext(r.Context(),
			`SELECT t.id FROM upgrade_tasks t WHERE `+strings.Join(where, " AND ")+
				` ORDER BY t.created_at DESC, t.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()
		ids := make([]int64, 0, 64)
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		truncated := len(ids) > task.MaxBatchDelete
		if truncated {
			ids = ids[:task.MaxBatchDelete]
		}
		httpx.OK(w, map[string]any{"ids": ids, "truncated": truncated, "limit": task.MaxBatchDelete})
	}
}

// TaskList 任务列表。
//
// 可选查询参数：kind（upgrade/command）、component_id（只看引用该组件的任务）、
// status（任务状态）。删除组件前可用 component_id 快速确认引用情况。
func TaskList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 租户隔离：只列出当前租户的任务（superadmin 跨租户）
		actor, _ := auth.FromContext(r.Context())
		mode := ""
		var mates map[string]bool
		if !actorIsPrivileged(actor) && actor != nil {
			mode = s.Task.AccessMode(r.Context(), actor.Username)
			if m, mErr := orgService(s).TeamMates(r.Context(), actor.Username); mErr == nil {
				mates = m
			}
		}
		where, args := taskListFilter(r, actor)

		// 服务端分页：默认 50，最大 200；页码越界夹紧到最后一页。
		page := atoiDefault(r.URL.Query().Get("page"), 1)
		if page < 1 {
			page = 1
		}
		size := atoiDefault(r.URL.Query().Get("size"), 50)
		if size < 1 {
			size = 50
		}
		if size > 200 {
			size = 200
		}
		whereSQL := strings.Join(where, " AND ")
		var total int
		if err := s.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM upgrade_tasks t WHERE `+whereSQL, args...).Scan(&total); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		pages := (total + size - 1) / size
		if pages < 1 {
			pages = 1
		}
		if page > pages {
			page = pages
		}
		args = append(args, size, (page-1)*size)
		limitAt, offsetAt := len(args)-1, len(args)
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT t.id, COALESCE(t.kind,'upgrade'), t.name, COALESCE(t.component_id,0), t.version, t.status, t.created_at,
			       COALESCE(t.created_by,''),
			       COUNT(th.id) AS total,
			       COUNT(th.id) FILTER (WHERE th.status='success') AS success,
			       COUNT(th.id) FILTER (WHERE th.status='failed') AS failed
			FROM upgrade_tasks t LEFT JOIN task_hosts th ON th.task_id=t.id
			WHERE `+whereSQL+`
			GROUP BY t.id ORDER BY t.created_at DESC
			LIMIT $`+strconv.Itoa(limitAt)+` OFFSET $`+strconv.Itoa(offsetAt)+``, args...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var id, cid int64
			var kind, name, version, status, createdBy string
			var createdAt any
			var total, success, failed int
			if err := rows.Scan(&id, &kind, &name, &cid, &version, &status, &createdAt,
				&createdBy, &total, &success, &failed); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			// 仅当策略为「仅本人」且非创建者时隐藏操作按钮；
			// off 模式下同团队校验由写接口完成（前端无法逐行判断团队关系）。
			canOperate := mode != "on" || createdBy == "" ||
				(actor != nil && createdBy == actor.Username)
			canView := actorIsPrivileged(actor) || createdBy == "" ||
				(actor != nil && createdBy == actor.Username) || mates[createdBy]
			list = append(list, map[string]any{
				"id": id, "kind": kind, "name": name, "component_id": cid, "version": version,
				"status": status, "created_at": createdAt, "created_by": createdBy,
				"can_operate": canOperate, "can_view": canView,
				"total": total, "success": success, "failed": failed,
			})
		}
		httpx.OK(w, map[string]any{
			"list": list, "total": total, "page": page, "size": size, "pages": pages,
		})
	}
}

// taskBatchDetail 批次进度。
type taskBatchDetail struct {
	BatchIndex int    `json:"batch_index"`
	Status     string `json:"status"`
	Total      int    `json:"total"`
	Success    int    `json:"success"`
	Failed     int    `json:"failed"`
	Done       int    `json:"done"`
	StartedAt  any    `json:"started_at"`
	FinishedAt any    `json:"finished_at"`
}

// taskHostDetail 逐机状态与当前阶段。
type taskHostDetail struct {
	ID          int64  `json:"id"`
	HostID      int64  `json:"host_id"`
	IP          string `json:"ip"`
	Hostname    string `json:"hostname"`
	Batch       int    `json:"batch"`
	Status      string `json:"status"`
	Stage       string `json:"stage"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	ExitCode    int    `json:"exit_code"`
	Attempts    int    `json:"attempts"`
	Log         string `json:"log"`
	AgentID     string `json:"agent_id"`
	AgentStatus string `json:"agent_status"`
	StartedAt   any    `json:"started_at"`
	FinishedAt  any    `json:"finished_at"`
}

// taskCommandDetail 指令状态与累计日志。
type taskCommandDetail struct {
	ID           int64           `json:"id"`
	TaskHostID   int64           `json:"task_host_id,omitempty"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	Attempts     int             `json:"attempts"`
	Log          string          `json:"log"`
	Result       json.RawMessage `json:"result,omitempty"`
	CreatedAt    any             `json:"created_at"`
	DispatchedAt any             `json:"dispatched_at"`
	StartedAt    any             `json:"started_at"`
	FinishedAt   any             `json:"finished_at"`
}

// nullableTime 将 sql.NullTime 转为 JSON 可序列化的 nil / time.Time。
func nullableTime(t sql.NullTime) any {
	if t.Valid {
		return t.Time
	}
	return nil
}

// TaskCreators 返回当前可见任务的创建者列表（去重，最多 500 个），供列表筛选。
//
// 非管理员看不到命令任务，创建者列表同样排除仅创建过命令任务的用户。
func TaskCreators(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, _ := auth.FromContext(r.Context())
		tCond, tArgs := reqScope(r).Where("t", 1)
		where := []string{tCond, "COALESCE(t.created_by,'') <> ''"}
		if !actorIsPrivileged(actor) {
			where = append(where, "COALESCE(t.kind,'upgrade') <> 'command'")
		}
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT DISTINCT t.created_by FROM upgrade_tasks t
			 WHERE `+strings.Join(where, " AND ")+`
			 ORDER BY t.created_by LIMIT 500`, tArgs...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()
		out := make([]string, 0, 32)
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			out = append(out, v)
		}
		if err := rows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, out)
	}
}

// TaskDetail 任务详情：批次进度、逐机状态、当前阶段与指令日志。
//
// 主机分页返回（默认 50、最大 500），只查询当前页主机的指令日志，
// 即使任务命中几万台主机，详情接口也不会一次性返回全部数据。
func TaskDetail(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		ctx := r.Context()

		var (
			name, version, status, createdBy, compName, compDisplay string
			kind                                                    string
			componentID                                             int64
			commandRaw                                              []byte
			createdAt, updatedAt                                    sql.NullTime
			selectorRaw, strategyRaw                                []byte
		)
		// 租户隔离：非本租户任务一律视为不存在（不泄露存在性）
		tCond, tArgs := reqScope(r).Where("t", 2)
		err := s.DB.QueryRowContext(ctx, `
			SELECT COALESCE(t.kind,'upgrade'), t.name, COALESCE(t.component_id,0), t.version, t.status, COALESCE(t.created_by,''),
			       t.created_at, t.updated_at, t.selector, t.strategy, COALESCE(t.command,'{}'::jsonb),
			       COALESCE(c.name,''), COALESCE(c.display_name,'')
			FROM upgrade_tasks t
			LEFT JOIN components c ON c.id=t.component_id
			WHERE t.id=$1 AND `+tCond,
			append([]any{id}, tArgs...)...).
			Scan(&kind, &name, &componentID, &version, &status, &createdBy,
				&createdAt, &updatedAt, &selectorRaw, &strategyRaw, &commandRaw, &compName, &compDisplay)
		if err == sql.ErrNoRows {
			httpx.Fail(w, http.StatusNotFound, "任务不存在")
			return
		}
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		// 命令任务（脚本/输出可能含敏感信息）仅管理员可见。
		if kind == "command" && !actorIsPrivileged(actorOf(r)) {
			httpx.Fail(w, http.StatusNotFound, "任务不存在")
			return
		}
		// 详情与执行日志仅创建者、同团队成员或管理员可见。
		allowed, viewErr := s.Task.CanViewTaskDetail(ctx, actorOf(r), createdBy)
		if viewErr != nil {
			httpx.FailErr(w, http.StatusInternalServerError, viewErr)
			return
		}
		if !allowed {
			httpx.Fail(w, http.StatusNotFound, "任务不存在")
			return
		}

		// 任务级汇总。
		var total, success, failed, done, active int
		if err := s.DB.QueryRowContext(ctx, `
			SELECT COUNT(*),
			       COUNT(*) FILTER (WHERE status='success'),
			       COUNT(*) FILTER (WHERE status IN ('failed','timeout')),
			       COUNT(*) FILTER (WHERE status IN ('success','failed','rolled_back','timeout','skipped','canceled')),
			       COUNT(*) FILTER (WHERE status IN ('pending','running'))
			FROM task_hosts WHERE task_id=$1`, id).
			Scan(&total, &success, &failed, &done, &active); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}

		// 批次进度（成功/失败/完成数从 task_hosts 实时计算）。
		brows, err := s.DB.QueryContext(ctx, `
			SELECT b.batch_index, b.status, b.total,
			       COUNT(th.id) FILTER (WHERE th.status='success'),
			       COUNT(th.id) FILTER (WHERE th.status IN ('failed','timeout')),
			       COUNT(th.id) FILTER (WHERE th.status IN ('success','failed','rolled_back','timeout','skipped','canceled')),
			       b.started_at, b.finished_at
			FROM task_batches b
			LEFT JOIN task_hosts th ON th.task_id=b.task_id AND th.batch_index=b.batch_index
			WHERE b.task_id=$1
			GROUP BY b.id, b.batch_index, b.status, b.total, b.started_at, b.finished_at
			ORDER BY b.batch_index`, id)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		batches := []taskBatchDetail{}
		for brows.Next() {
			var b taskBatchDetail
			var started, finished sql.NullTime
			if err := brows.Scan(&b.BatchIndex, &b.Status, &b.Total, &b.Success, &b.Failed, &b.Done,
				&started, &finished); err != nil {
				brows.Close()
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			b.StartedAt, b.FinishedAt = nullableTime(started), nullableTime(finished)
			batches = append(batches, b)
		}
		brows.Close()
		if err := brows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}

		// 主机分页：默认 50，最大 500；页码越界夹紧到最后一页。
		page := atoiDefault(r.URL.Query().Get("host_page"), 1)
		size := atoiDefault(r.URL.Query().Get("host_size"), 50)
		if size > 500 {
			size = 500
		}
		pages := (total + size - 1) / size
		if pages < 1 {
			pages = 1
		}
		if page > pages {
			page = pages
		}
		offset := (page - 1) * size

		hrows, err := s.DB.QueryContext(ctx, `
			SELECT th.id, th.host_id, h.ip, COALESCE(h.hostname,''),
			       th.batch_index, th.status, COALESCE(th.stage,''),
			       COALESCE(th.from_version,''), COALESCE(th.to_version,''),
			       COALESCE(th.exit_code,0), th.attempts, COALESCE(th.log,''),
			       th.started_at, th.finished_at,
			       COALESCE(h.agent_id,''), COALESCE(h.agent_status,'')
			FROM task_hosts th
			JOIN hosts h ON h.id=th.host_id
			WHERE th.task_id=$1
			ORDER BY th.batch_index, h.ip
			LIMIT $2 OFFSET $3`, id, size, offset)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		hosts := []taskHostDetail{}
		hostIDs := make([]int64, 0, size)
		for hrows.Next() {
			var h taskHostDetail
			var started, finished sql.NullTime
			if err := hrows.Scan(&h.ID, &h.HostID, &h.IP, &h.Hostname, &h.Batch, &h.Status, &h.Stage,
				&h.FromVersion, &h.ToVersion, &h.ExitCode, &h.Attempts, &h.Log,
				&started, &finished, &h.AgentID, &h.AgentStatus); err != nil {
				hrows.Close()
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			h.StartedAt, h.FinishedAt = nullableTime(started), nullableTime(finished)
			hosts = append(hosts, h)
			hostIDs = append(hostIDs, h.ID)
		}
		hrows.Close()
		if err := hrows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}

		// 只返回当前页主机的指令日志，避免大任务响应体膨胀。
		commands := []taskCommandDetail{}
		if len(hostIDs) > 0 {
			crows, err := s.DB.QueryContext(ctx, `
				SELECT id, COALESCE(task_host_id,0), type, status, attempts, COALESCE(log,''), result,
				       created_at, dispatched_at, started_at, finished_at
				FROM commands
				WHERE task_host_id = ANY($1)
				ORDER BY id`, pq.Array(hostIDs))
			if err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			for crows.Next() {
				var c taskCommandDetail
				var result []byte
				var created, dispatched, started, finished sql.NullTime
				if err := crows.Scan(&c.ID, &c.TaskHostID, &c.Type, &c.Status, &c.Attempts, &c.Log,
					&result, &created, &dispatched, &started, &finished); err != nil {
					crows.Close()
					httpx.FailErr(w, http.StatusInternalServerError, err)
					return
				}
				if len(result) > 0 {
					c.Result = json.RawMessage(result)
				}
				c.CreatedAt, c.DispatchedAt = nullableTime(created), nullableTime(dispatched)
				c.StartedAt, c.FinishedAt = nullableTime(started), nullableTime(finished)
				commands = append(commands, c)
			}
			crows.Close()
			if err := crows.Err(); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
		}

		if len(selectorRaw) == 0 {
			selectorRaw = []byte("{}")
		}
		if len(strategyRaw) == 0 {
			strategyRaw = []byte("{}")
		}
		if len(commandRaw) == 0 {
			commandRaw = []byte("{}")
		}
		actor, _ := auth.FromContext(ctx)
		canOperate := true
		if !actorIsPrivileged(actor) && actor != nil {
			if s.Task.AccessMode(ctx, actor.Username) == "on" &&
				createdBy != "" && createdBy != actor.Username {
				canOperate = false
			}
		}
		httpx.OK(w, map[string]any{
			"id": id, "kind": kind, "name": name, "component_id": componentID,
			"component_name": compName, "component_display_name": compDisplay,
			"version": version, "status": status, "created_by": createdBy,
			"can_operate": canOperate,
			"created_at":  nullableTime(createdAt), "updated_at": nullableTime(updatedAt),
			"selector": json.RawMessage(selectorRaw), "strategy": json.RawMessage(strategyRaw),
			"command": json.RawMessage(commandRaw),
			"total":   total, "success": success, "failed": failed, "done": done, "active": active,
			"batches": batches, "hosts": hosts,
			"host_total": total, "host_page": page, "host_size": size, "host_pages": pages,
			"commands": commands,
		})
	}
}

// TaskStart / TaskPause / TaskCancel / TaskDelete / TaskPromote 统一走 taskAction。
func TaskStart(s *svc.ServiceContext) http.HandlerFunc {
	return taskAction(s, audit.ActionTaskStart, func(id int64, r *http.Request) error { return s.Task.Start(r.Context(), reqScope(r), id) })
}
func TaskPause(s *svc.ServiceContext) http.HandlerFunc {
	return taskAction(s, audit.ActionTaskPause, func(id int64, r *http.Request) error { return s.Task.Pause(r.Context(), reqScope(r), id) })
}
func TaskCancel(s *svc.ServiceContext) http.HandlerFunc {
	return taskAction(s, audit.ActionTaskCancel, func(id int64, r *http.Request) error { return s.Task.Cancel(r.Context(), reqScope(r), id) })
}

// TaskRetry 重新执行失败/卡住的主机（可指定 task_host_ids，为空则全部可重试）。
func TaskRetry(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		id, _ := strconv.ParseInt(idStr, 10, 64)
		var req task.RetryRequest
		if r.ContentLength != 0 {
			if err := httpx.DecodeJSON(r, &req); err != nil && err != io.EOF {
				httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
				return
			}
		}
		res, err := s.Task.Retry(r.Context(), reqScope(r), id, req)
		if err != nil {
			auditOp(s, r, audit.ActionTaskRetry, "task", idStr, "", err)
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		auditOp(s, r, audit.ActionTaskRetry, "task", idStr, "", nil)
		httpx.OK(w, res)
	}
}

// TaskDelete 删除任务及其批次、逐机记录和历史指令。
// 仅允许删除 draft 或终态任务；running/paused 需先取消，避免删除仍在执行的升级。
func TaskDelete(s *svc.ServiceContext) http.HandlerFunc {
	return taskAction(s, audit.ActionTaskDelete, func(id int64, r *http.Request) error {
		return s.Task.Delete(r.Context(), reqScope(r), id)
	})
}

// TaskBatchDelete 批量删除任务：POST /api/tasks/batch-delete {"ids":[1,2,3]}。
//
// 一次请求完成多个删除，逐个复用 Delete 的校验；每个任务单独写审计，
// 返回成功列表与逐个失败原因（部分失败也返回 200）。
func TaskBatchDelete(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []int64 `json:"ids"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		res, err := s.Task.BatchDelete(r.Context(), reqScope(r), req.IDs)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		for _, id := range res.Deleted {
			auditOp(s, r, audit.ActionTaskDelete, "task", strconv.FormatInt(id, 10), "", nil)
		}
		for _, f := range res.Failed {
			auditOp(s, r, audit.ActionTaskDelete, "task", strconv.FormatInt(f.ID, 10), "", errors.New(f.Error))
		}
		httpx.OK(w, res)
	}
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
