package handler

import (
	"net/http"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// HostGroupOptions 返回主机业务分组的可选值（去重），供授权表单下拉多选。
//
// 数据来源 hosts.app_code / hosts.biz_group；空值忽略。
// 返回 {"app_code":[...], "biz_group":[...]}，便于前端分两组展示。
func HostGroupOptions(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tCond, tArgs := reqScope(r).Where("", 1)
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT DISTINCT app_code FROM hosts
			 WHERE COALESCE(app_code,'') <> '' AND `+tCond+`
			UNION
			SELECT DISTINCT biz_group FROM hosts
			 WHERE COALESCE(biz_group,'') <> '' AND `+tCond+`
			ORDER BY 1`, tArgs...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()
		all := make([]string, 0, 64)
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err == nil {
				if v = strings.TrimSpace(v); v != "" {
					all = append(all, v)
				}
			}
		}
		if err := rows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{"groups": all})
	}
}
