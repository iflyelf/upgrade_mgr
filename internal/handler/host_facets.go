package handler

import (
	"net/http"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// HostFacets 返回任务选主机可用的字段及其去重值（租户隔离，任何认证用户可访问）。
//
// 返回格式：{"fields": ["app_code", "biz_group"], "values": {"app_code": ["...", ...], "biz_group": [...]}}.
// 字段列表取自 task.host_selector_fields 设置；为空时返回空列表。
func HostFacets(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fieldsStr := ""
		if s.Settings != nil {
			snap := s.Settings.Snapshot()
			fieldsStr = strings.TrimSpace(snap.Task.HostSelectorFields)
		}
		// ?fields= 显式指定字段（主机页筛选下拉用），不受任务选主机配置影响；
		// 仍经下方列名白名单过滤。
		if v := strings.TrimSpace(r.URL.Query().Get("fields")); v != "" {
			fieldsStr = v
		}
		if fieldsStr == "" {
			httpx.OK(w, map[string]any{"fields": []string{}, "values": map[string][]string{}})
			return
		}

		// 解析配置的字段列表，过滤掉非法列名（防止 SQL 注入）。
		allowedCols := map[string]bool{
			"app_code": true, "os_family": true, "os_type": true, "account": true,
			"biz_group": true, "department": true, "vendor": true, "arch": true, "host_type": true,
		}
		var fields []string
		seen := map[string]bool{}
		for _, f := range strings.Split(fieldsStr, ",") {
			f = strings.TrimSpace(f)
			if f != "" && allowedCols[f] && !seen[f] {
				seen[f] = true
				fields = append(fields, f)
			}
		}
		if len(fields) == 0 {
			httpx.OK(w, map[string]any{"fields": []string{}, "values": map[string][]string{}})
			return
		}

		tCond, tArgs := reqScope(r).Where("", 1)
		values := make(map[string][]string, len(fields))
		for _, col := range fields {
			q := "SELECT DISTINCT " + col + " FROM hosts WHERE COALESCE(" + col + ",'') <> '' AND " + tCond + " ORDER BY 1"
			rows, err := s.DB.QueryContext(r.Context(), q, tArgs...)
			if err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			var list []string
			for rows.Next() {
				var v string
				if err := rows.Scan(&v); err == nil {
					if v = strings.TrimSpace(v); v != "" {
						list = append(list, v)
					}
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			values[col] = list
		}

		httpx.OK(w, map[string]any{"fields": fields, "values": values})
	}
}
