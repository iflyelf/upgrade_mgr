package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	synclogic "github.com/iflyelf/upgrade_mgr/internal/logic/sync"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// Host 主机列表行。
type Host struct {
	ID           int64  `json:"id"`
	IP           string `json:"ip"`
	Hostname     string `json:"hostname"`
	OSFamily     string `json:"os_family"`
	OSType       string `json:"os_type"`
	Arch         string `json:"arch"`
	Account      string `json:"account"`
	AppCode      string `json:"app_code"`
	BizGroup     string `json:"biz_group"`
	Department   string `json:"department"`
	Vendor       string `json:"vendor"`
	HostType     string `json:"host_type"`
	AgentVersion string `json:"agent_version"`
	AgentStatus  string `json:"agent_status"`
	Source       string `json:"source"`
}

// ListHosts 主机列表，支持关键字与分组过滤（?q=&app_code=&os_family=&page=&size=）。
func ListHosts(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// 租户隔离：恒为第一个条件，确保任何过滤组合下都不会漏过滤。
		tCond, tArgs := reqScope(r).Where("", 1)
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
			where = append(where,
				"(ip ILIKE $"+strconv.Itoa(i)+" OR hostname ILIKE $"+strconv.Itoa(i)+
					" OR biz_group ILIKE $"+strconv.Itoa(i)+")")
			args = append(args, "%"+kw+"%")
			i++
		}
		add("app_code = ", q.Get("app_code"))
		add("os_family = ", q.Get("os_family"))
		add("account = ", q.Get("account"))
		add("agent_status = ", q.Get("agent_status"))

		whereSQL := ""
		if len(where) > 0 {
			whereSQL = " WHERE " + strings.Join(where, " AND ")
		}

		page := atoiDefault(q.Get("page"), 1)
		size := atoiDefault(q.Get("size"), 20)
		if size > 500 {
			size = 500
		}
		offset := (page - 1) * size

		var total int
		if err := s.DB.QueryRowContext(r.Context(),
			"SELECT COUNT(1) FROM hosts"+whereSQL, args...).Scan(&total); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}

		listArgs := append(append([]any{}, args...), size, offset)
		rows, err := s.DB.QueryContext(r.Context(),
			`SELECT id, ip, hostname, os_family, os_type, arch, account, app_code,
			        biz_group, department, vendor, host_type, agent_version, agent_status, source
			   FROM hosts`+whereSQL+
				" ORDER BY ip LIMIT $"+strconv.Itoa(i)+" OFFSET $"+strconv.Itoa(i+1), listArgs...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()

		list := []Host{}
		for rows.Next() {
			var h Host
			if err := rows.Scan(&h.ID, &h.IP, &h.Hostname, &h.OSFamily, &h.OSType, &h.Arch,
				&h.Account, &h.AppCode, &h.BizGroup, &h.Department, &h.Vendor, &h.HostType,
				&h.AgentVersion, &h.AgentStatus, &h.Source); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			list = append(list, h)
		}
		httpx.OK(w, map[string]any{"total": total, "list": list, "page": page, "size": size})
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
		res, err := s.Syncer.SyncFromConsul(r.Context(), cli)
		if errors.Is(err, synclogic.ErrSyncInProgress) {
			httpx.Fail(w, http.StatusConflict, "已有同步任务正在执行，请稍后重试")
			return
		}
		if err != nil {
			httpx.FailErr(w, http.StatusBadGateway, fmt.Errorf("同步失败: %w", err))
			return
		}
		httpx.OK(w, map[string]any{
			"services": res.Services, "instances": res.Instances,
			"upserted": res.Upserted, "duration": res.Duration.String(),
		})
	}
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}
