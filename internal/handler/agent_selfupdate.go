package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/agentupdate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

type agentSelfUpdateRequest struct {
	HostIDs     []int64 `json:"host_ids"`
	AllOutdated bool    `json:"all_outdated"`
	Force       bool    `json:"force"`
	Variant     string  `json:"variant"`
}

// AgentSelfUpdate 管理员向指定主机或全部旧 Agent 下发自升级指令。
func AgentSelfUpdate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req agentSelfUpdateRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if len(req.HostIDs) == 0 && !req.AllOutdated {
			httpx.Fail(w, http.StatusBadRequest, "请选择主机，或勾选升级全部旧 Agent")
			return
		}
		snap := s.Settings.Snapshot()
		baseURL := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
		resolver := agentUpdateResolver(s)
		res, err := agentupdate.Enqueue(r.Context(), s.DB, reqScope(r), resolver, agentupdate.EnqueueRequest{
			HostIDs: req.HostIDs, AllOutdated: req.AllOutdated,
			Force: req.Force, Variant: req.Variant,
		}, baseURL)
		if err != nil {
			auditOp(s, r, audit.ActionAgentSelfUpdate, "agent", "", "", err)
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		auditOp(s, r, audit.ActionAgentSelfUpdate, "agent", "",
			fmt.Sprintf("enqueued=%d skipped=%d target=%s", res.Enqueued, res.SkippedCount, res.TargetVersion), nil)
		httpx.OK(w, res)
	}
}

type selfUpdateRecent struct {
	ID         int64      `json:"id"`
	AgentID    string     `json:"agent_id"`
	IP         string     `json:"ip"`
	Hostname   string     `json:"hostname"`
	Status     string     `json:"status"`
	ToVersion  string     `json:"to_version"`
	Log        string     `json:"log"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// AgentSelfUpdateStatus 返回服务端目标版本、可用二进制与最近自升级记录。
func AgentSelfUpdateStatus(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := s.Settings.Snapshot()
		baseURL := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
		resolver := agentUpdateResolver(s)

		hCond, hArgs := reqScope(r).Where("h", 1)
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT c.id, c.agent_id, h.ip, COALESCE(h.hostname,''), c.status,
			       COALESCE(c.payload->>'to_version',''), COALESCE(c.log,''),
			       c.created_at, c.finished_at
			FROM commands c
			JOIN hosts h ON h.agent_id = c.agent_id
			WHERE c.type='self_update' AND `+hCond+`
			ORDER BY c.id DESC LIMIT 50`, hArgs...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()

		recent := []selfUpdateRecent{}
		for rows.Next() {
			var (
				it       selfUpdateRecent
				finished sql.NullTime
			)
			if err := rows.Scan(&it.ID, &it.AgentID, &it.IP, &it.Hostname, &it.Status,
				&it.ToVersion, &it.Log, &it.CreatedAt, &finished); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			if finished.Valid {
				t := finished.Time
				it.FinishedAt = &t
			}
			recent = append(recent, it)
		}
		if err := rows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{
			"version":     resolver.Version,
			"auto_update": snap.Agent.AutoUpdate,
			"binaries":    resolver.Available(baseURL),
			"recent":      recent,
		})
	}
}
