package handler

import (
	"net/http"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// HostUpgradeHistory 主机升级历史（task_hosts 关联 tasks，按时间倒序）。
func HostUpgradeHistory(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := r.PathValue("ip")
		if ip == "" {
			httpx.Fail(w, http.StatusBadRequest, "缺少 ip")
			return
		}
		// 租户隔离：按主机所属租户过滤，防止跨租户窥探他人升级历史
		hCond, hArgs := reqScope(r).Where("h", 2)
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT t.id, t.name, t.version, th.status, th.from_version, th.to_version,
			       th.started_at, th.finished_at, th.exit_code, th.stage
			FROM task_hosts th
			JOIN upgrade_tasks t ON t.id = th.task_id
			JOIN hosts h ON h.id = th.host_id
			WHERE h.ip = $1 AND `+hCond+`
			ORDER BY th.created_at DESC
			LIMIT 50`, append([]any{ip}, hArgs...)...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var tid int64
			var name, version, status, fromV, toV, stage string
			var startedAt, finishedAt any
			var exitCode int
			if err := rows.Scan(&tid, &name, &version, &status, &fromV, &toV, &startedAt, &finishedAt, &exitCode, &stage); err != nil {
				continue
			}
			list = append(list, map[string]any{
				"task_id": tid, "task_name": name, "version": version,
				"status": status, "from_version": fromV, "to_version": toV,
				"started_at": startedAt, "finished_at": finishedAt,
				"exit_code": exitCode, "stage": stage,
			})
		}
		httpx.OK(w, list)
	}
}
