package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"

	synclogic "github.com/iflyelf/upgrade_mgr/internal/logic/sync"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// Host 主机列表行。
type Host struct {
	ID int64  `json:"id"`
	IP string `json:"ip"`
	// ExitIP 是 Agent 探测的本机出口 IP（多网卡时区别于注册来源地址）。
	ExitIP string `json:"exit_ip"`
	// TunnelIP 是主机侧隧道 IP（展示/筛选）。
	TunnelIP string `json:"tunnel_ip"`
	// ServerURLOverride 是连接地址覆盖：非空时服务端下发给 Agent 切换连接入口。
	ServerURLOverride string `json:"server_url_override"`
	AgentServerURL    string `json:"agent_server_url"`
	DefaultServerURL  string `json:"agent_default_server_url"`
	// 隧道自检结果：nil 表示未检查/已清空；false 时列表标红。
	TunnelCheckOK        *bool      `json:"tunnel_check_ok"`
	TunnelCheckSource    string     `json:"tunnel_check_source"`
	TunnelCheckAt        *time.Time `json:"tunnel_check_at,omitempty"`
	TunnelCheckLatencyMS int        `json:"tunnel_check_latency_ms"`
	TunnelCheckDetail    string     `json:"tunnel_check_detail"`
	// CustomFields 主机自定义字段值（键见 host_field_defs）。
	CustomFields      map[string]string `json:"custom_fields,omitempty"`
	Hostname          string            `json:"hostname"`
	OSFamily          string            `json:"os_family"`
	OSType            string            `json:"os_type"`
	Arch              string            `json:"arch"`
	Account           string            `json:"account"`
	AppCode           string            `json:"app_code"`
	BizGroup          string            `json:"biz_group"`
	Department        string            `json:"department"`
	Vendor            string            `json:"vendor"`
	HostType          string            `json:"host_type"`
	AgentID           string            `json:"agent_id"`
	AgentVersion      string            `json:"agent_version"`
	AgentBuildVersion string            `json:"agent_build_version"`
	AgentBuildHash    string            `json:"agent_build_hash"`
	AgentBuildVariant string            `json:"agent_build_variant"`
	AgentStatus       string            `json:"agent_status"`
	AgentUpdateStatus string            `json:"agent_update_status,omitempty"`
	AgentUpdateStage  string            `json:"agent_update_stage,omitempty"`
	AgentUpdateLog    string            `json:"agent_update_log,omitempty"`
	AgentUpdateAt     *time.Time        `json:"agent_update_at,omitempty"`
	Source            string            `json:"source"`
}

// hostOfflineAfter 返回离线判定阈值；解析失败时回退默认 1m，避免配置错误导致列表失效。
func hostOfflineAfter(s *svc.ServiceContext) time.Duration {
	offlineAfter := "1m"
	if s.Settings != nil {
		offlineAfter = s.Settings.Snapshot().Agent.OfflineAfter
	}
	d, err := time.ParseDuration(strings.TrimSpace(offlineAfter))
	if err != nil || d < 1*time.Second {
		d = 1 * time.Minute
	}
	return d
}

// hostListFilter 按查询参数构造主机列表的 WHERE 子句（表别名 h）。
//
// 列表与「按筛选条件全选」共用，保证两者命中的主机集合一致。
// 返回 WHERE 子句、参数，以及下一个可用的占位符序号。
func hostListFilter(s *svc.ServiceContext, r *http.Request) (string, []any, int) {
	offlineInterval := fmt.Sprintf("'%.0f seconds'", hostOfflineAfter(s).Seconds())

	q := r.URL.Query()
	// 租户隔离：恒为第一个条件，确保任何过滤组合下都不会漏过滤。
	tCond, tArgs := reqScope(r).Where("h", 1)
	var (
		where = []string{tCond}
		args  = append([]any{}, tArgs...)
		i     = len(tArgs) + 1
	)
	add := func(cond, val string) {
		if val == "" {
			return
		}
		where = append(where, cond+"$"+strconv.Itoa(i))
		args = append(args, val)
		i++
	}
	if kw := strings.TrimSpace(q.Get("q")); kw != "" {
		// 出口 IP / 隧道地址也参与搜索：运维通常按实际业务网地址找主机。
		where = append(where,
			"(h.ip ILIKE $"+strconv.Itoa(i)+" OR h.exit_ip ILIKE $"+strconv.Itoa(i)+
				" OR h.tunnel_ip ILIKE $"+strconv.Itoa(i)+
				" OR h.server_url_override ILIKE $"+strconv.Itoa(i)+
				" OR h.hostname ILIKE $"+strconv.Itoa(i)+
				" OR h.biz_group ILIKE $"+strconv.Itoa(i)+")")
		args = append(args, "%"+kw+"%")
		i++
	}
	add("h.app_code = ", q.Get("app_code"))
	add("h.os_family = ", q.Get("os_family"))
	add("h.account = ", q.Get("account"))

	// agent_status 过滤：支持按计算出的有效状态过滤。
	// 如果过滤值是 offline，需要用 CASE 表达式判断心跳超时。
	if statusFilter := q.Get("agent_status"); statusFilter != "" {
		if statusFilter == "offline" {
			where = append(where,
				"(h.agent_status='offline' OR h.agent_id='' OR h.last_seen IS NULL OR h.last_seen < NOW() - INTERVAL "+offlineInterval+")")
		} else {
			where = append(where, "h.agent_status=$"+strconv.Itoa(i))
			args = append(args, statusFilter)
			i++
		}
	}
	return " WHERE " + strings.Join(where, " AND "), args, i
}

// ListHosts 主机列表，支持关键字与分组过滤（?q=&app_code=&os_family=&page=&size=）。
func ListHosts(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := hostOfflineAfter(s)
		q := r.URL.Query()
		whereSQL, args, i := hostListFilter(s, r)

		page := atoiDefault(q.Get("page"), 1)
		size := atoiDefault(q.Get("size"), 20)
		if size > 500 {
			size = 500
		}
		// 防止 page 极大时 (page-1)*size 溢出；count 之后再按实际总页数夹紧。
		if page > 10_000_000 {
			page = 10_000_000
		}

		var total int
		if err := s.DB.QueryRowContext(r.Context(),
			"SELECT COUNT(1) FROM hosts h"+whereSQL, args...).Scan(&total); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		pages := (total + size - 1) / size
		if pages < 1 {
			pages = 1
		}
		if page > pages {
			// 删除最后一页数据或用户直接改 URL 时，落到最后一页而不是返回空列表。
			page = pages
		}
		offset := (page - 1) * size

		listArgs := append(append([]any{}, args...), size, offset)
		rows, err := s.DB.QueryContext(r.Context(),
			`SELECT h.id, h.ip, COALESCE(h.exit_ip,''), COALESCE(h.tunnel_ip,''),
			        COALESCE(h.server_url_override,''),
			        COALESCE(h.agent_server_url,''), COALESCE(h.agent_default_server_url,''),
			        h.tunnel_check_ok, COALESCE(h.tunnel_check_source,''), h.tunnel_check_at,
			        COALESCE(h.tunnel_check_latency_ms,0), COALESCE(h.tunnel_check_detail,''),
			        COALESCE(h.custom_fields,'{}'::jsonb),
			        h.hostname, h.os_family, h.os_type, h.arch, h.account, h.app_code,
			        h.biz_group, h.department, h.vendor, h.host_type, COALESCE(h.agent_id,''),
			        h.agent_version, COALESCE(h.agent_build_version,''), COALESCE(h.agent_build_hash,''),
			        COALESCE(h.agent_build_variant,''), h.agent_status, h.source, h.last_seen
			   FROM hosts h`+whereSQL+
				" ORDER BY h.ip LIMIT $"+strconv.Itoa(i)+" OFFSET $"+strconv.Itoa(i+1), listArgs...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()

		list := []Host{}
		for rows.Next() {
			var h Host
			var checkOK sql.NullBool
			var checkAt sql.NullTime
			var lastSeen sql.NullTime
			var customRaw []byte
			if err := rows.Scan(&h.ID, &h.IP, &h.ExitIP, &h.TunnelIP, &h.ServerURLOverride,
				&h.AgentServerURL, &h.DefaultServerURL,
				&checkOK, &h.TunnelCheckSource, &checkAt,
				&h.TunnelCheckLatencyMS, &h.TunnelCheckDetail, &customRaw,
				&h.Hostname, &h.OSFamily, &h.OSType, &h.Arch,
				&h.Account, &h.AppCode, &h.BizGroup, &h.Department, &h.Vendor, &h.HostType,
				&h.AgentID, &h.AgentVersion, &h.AgentBuildVersion, &h.AgentBuildHash,
				&h.AgentBuildVariant, &h.AgentStatus, &h.Source, &lastSeen); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			// 计算有效状态：如果 last_seen 超时或为空（且有 agent_id），覆盖为 offline。
			// 没有 agent_id 的主机（来自 Consul 同步）保持 unknown。
			if h.AgentID != "" {
				if !lastSeen.Valid || time.Since(lastSeen.Time) > d {
					h.AgentStatus = "offline"
				}
			}
			if checkOK.Valid {
				v := checkOK.Bool
				h.TunnelCheckOK = &v
			}
			if checkAt.Valid {
				t := checkAt.Time
				h.TunnelCheckAt = &t
			}
			if len(customRaw) > 0 {
				_ = json.Unmarshal(customRaw, &h.CustomFields)
			}
			list = append(list, h)
		}
		if err := rows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}

		// 当前页 Agent 的最近一次自升级状态（单独查询，避免主机列表 JOIN commands 放大行数）。
		agentIDs := make([]string, 0, len(list))
		for _, h := range list {
			if h.AgentID != "" {
				agentIDs = append(agentIDs, h.AgentID)
			}
		}
		type updateInfo struct {
			Status string
			Stage  string
			Log    string
			At     time.Time
		}
		statuses := map[string]updateInfo{}
		if len(agentIDs) > 0 {
			srows, err := s.DB.QueryContext(r.Context(), `
				SELECT DISTINCT ON (agent_id) agent_id, status,
				       COALESCE(last_stage,''),
				       left(COALESCE(log,''), 4096),
				       COALESCE(finished_at, last_stage_at, created_at)
				FROM commands
				WHERE agent_id = ANY($1) AND type='self_update'
				ORDER BY agent_id, id DESC`, pq.Array(agentIDs))
			if err == nil {
				defer srows.Close()
				for srows.Next() {
					var id string
					var info updateInfo
					if err := srows.Scan(&id, &info.Status, &info.Stage, &info.Log, &info.At); err == nil {
						statuses[id] = info
					}
				}
				// 自升级状态属于附加信息，查询失败不影响主机列表返回。
				_ = srows.Err()
			}
		}
		for idx := range list {
			if v, ok := statuses[list[idx].AgentID]; ok {
				at := v.At
				list[idx].AgentUpdateStatus = v.Status
				list[idx].AgentUpdateStage = v.Stage
				list[idx].AgentUpdateLog = v.Log
				list[idx].AgentUpdateAt = &at
			}
		}

		targetVersion := s.Version
		if targetVersion == "" {
			targetVersion = "dev"
		}
		autoUpdate := false
		if s.Settings != nil {
			autoUpdate = s.Settings.Snapshot().Agent.AutoUpdate
		}
		httpx.OK(w, map[string]any{
			"total": total, "list": list, "page": page, "size": size, "pages": pages,
			"agent_target_version": targetVersion,
			"agent_auto_update":    autoUpdate,
		})
	}
}

// SyncHosts 手动触发 Consul 同步（可选依赖，未启用返回提示）。
func SyncHosts(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cli := s.Consul()
		if cli == nil {
			httpx.Fail(w, http.StatusServiceUnavailable, "Consul 未启用或不可用，可在设置页配置后重试")
			return
		}
		snap := s.Settings.Snapshot()
		res, err := s.Syncer.SyncFromConsul(r.Context(), cli,
			snap.Consul.CleanupMissing, snap.Consul.Concurrency)
		if errors.Is(err, synclogic.ErrSyncInProgress) {
			httpx.Fail(w, http.StatusConflict, "已有同步任务正在执行，请稍后重试")
			return
		}
		if err != nil {
			auditOp(s, r, audit.ActionHostSync, "host", "", "", err)
			httpx.FailErr(w, http.StatusBadGateway, fmt.Errorf("同步失败: %w", err))
			return
		}
		auditOpDetails(s, r, audit.ActionHostSync, "host", "", "consul", nil,
			map[string]any{
				"services": res.Services, "instances": res.Instances,
				"upserted": res.Upserted, "deleted": res.Deleted,
				"partial": res.Partial, "cleanup_skipped": res.CleanupSkipped,
			}, nil)
		httpx.OK(w, map[string]any{
			"services": res.Services, "instances": res.Instances,
			"upserted": res.Upserted, "deleted": res.Deleted,
			"partial": res.Partial, "cleanup_skipped": res.CleanupSkipped,
			"duration": res.Duration.String(),
		})
	}
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}
