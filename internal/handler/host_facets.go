package handler

import (
	"net/http"
	"strconv"
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

		// 主机页 app_code/业务组等候选值可能非常多：支持关键字过滤 + 限制返回条数，
		// 前端输入时按需请求（配合 pg_trgm 索引，避免一次拉取全量拖慢页面）。
		// limit 不传时保持原有「返回全部」行为（任务页选主机依赖完整候选值）；
		// 主机页显式传 limit，并在输入关键字时带 q 按需搜索。
		kw := strings.TrimSpace(r.URL.Query().Get("q"))
		limit := 0
		if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		if limit < 0 {
			limit = 0
		}
		if limit > 1000 {
			limit = 1000
		}

		tCond, tArgs := reqScope(r).Where("", 1)
		values := make(map[string][]string, len(fields))
		truncated := make(map[string]bool, len(fields))
		for _, col := range fields {
			q := "SELECT DISTINCT " + col + " FROM hosts WHERE COALESCE(" + col + ",'') <> '' AND " + tCond
			args := append([]any{}, tArgs...)
			if kw != "" {
				q += " AND " + col + " ILIKE $" + strconv.Itoa(len(args)+1)
				args = append(args, "%"+kw+"%")
			}
			// 多取一条用于判断是否还有更多候选值（前端可提示继续输入缩小范围）。
			if limit > 0 {
				q += " ORDER BY 1 LIMIT $" + strconv.Itoa(len(args)+1)
				args = append(args, limit+1)
			} else {
				q += " ORDER BY 1"
			}

			rows, err := s.DB.QueryContext(r.Context(), q, args...)
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
			if limit > 0 && len(list) > limit {
				list = list[:limit]
				truncated[col] = true
			}
			values[col] = list
		}

		httpx.OK(w, map[string]any{
			"fields": fields, "values": values, "truncated": truncated,
			"q": kw, "limit": limit,
		})
	}
}
