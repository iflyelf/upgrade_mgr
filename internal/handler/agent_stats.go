package handler

import (
	"net/http"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// AgentStats 返回 Agent 版本/OS/架构分布、离线数与过期数。
//
// 用于主机页「Agent 版本分布」卡片：升级 Agent 时快速确认剩余旧版本数量。
func AgentStats(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tCond, tArgs := reqScope(r).Where("h", 1)
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT COALESCE(a.agent_version,''), COALESCE(a.os,''), COALESCE(a.arch,''),
			       COUNT(*) AS total,
			       COUNT(*) FILTER (
			           WHERE a.status='offline'
			              OR a.last_heartbeat_at IS NULL
			              OR a.last_heartbeat_at < NOW() - INTERVAL '5 minutes'
			       ) AS offline
			  FROM agents a
			  JOIN hosts h ON h.id = a.host_id
			 WHERE `+tCond+`
			 GROUP BY 1,2,3
			 ORDER BY total DESC`, tArgs...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()

		target := s.Version
		if target == "" {
			target = "dev"
		}
		list := make([]map[string]any, 0, 16)
		total, offline, outdated := 0, 0, 0
		for rows.Next() {
			var version, osName, arch string
			var count, off int
			if err := rows.Scan(&version, &osName, &arch, &count, &off); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			isOld := version != "" && version != target
			list = append(list, map[string]any{
				"version": version, "os": osName, "arch": arch,
				"count": count, "offline": off, "outdated": isOld,
			})
			total += count
			offline += off
			if isOld {
				outdated += count
			}
		}
		if err := rows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{
			"target_version": target, "total": total, "offline": offline,
			"outdated": outdated, "list": list,
		})
	}
}
