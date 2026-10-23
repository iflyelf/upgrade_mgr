package handler

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// auditFilter 构造审计日志查询条件（列表与导出共用）。
func auditFilter(r *http.Request) (string, []any, error) {
	tCond, tArgs := reqScope(r).Where("a", 1)
	where := []string{tCond}
	args := append([]any{}, tArgs...)

	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		args = append(args, "%"+q+"%")
		n := len(args)
		where = append(where, fmt.Sprintf(
			"(COALESCE(a.username,'') ILIKE $%d OR COALESCE(a.resource_name,'') ILIKE $%d"+
				" OR COALESCE(a.resource_id,'') ILIKE $%d OR COALESCE(a.action,'') ILIKE $%d)",
			n, n, n, n))
	}
	exact := []struct{ param, col string }{
		{"action", "a.action"},
		{"username", "a.username"},
		{"resource_type", "a.resource_type"},
		{"status", "a.status"},
	}
	for _, f := range exact {
		if v := strings.TrimSpace(r.URL.Query().Get(f.param)); v != "" {
			args = append(args, v)
			where = append(where, fmt.Sprintf("%s=$%d", f.col, len(args)))
		}
	}
	if from := strings.TrimSpace(r.URL.Query().Get("from")); from != "" {
		if _, err := time.Parse("2006-01-02", from); err != nil {
			return "", nil, fmt.Errorf("开始日期格式应为 YYYY-MM-DD")
		}
		args = append(args, from)
		where = append(where, fmt.Sprintf("a.created_at >= $%d::date", len(args)))
	}
	if to := strings.TrimSpace(r.URL.Query().Get("to")); to != "" {
		if _, err := time.Parse("2006-01-02", to); err != nil {
			return "", nil, fmt.Errorf("结束日期格式应为 YYYY-MM-DD")
		}
		args = append(args, to)
		where = append(where, fmt.Sprintf("a.created_at < ($%d::date + INTERVAL '1 day')", len(args)))
	}
	return strings.Join(where, " AND "), args, nil
}

// AuditLogList 审计日志列表（管理员，租户隔离）。
func AuditLogList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		whereSQL, args, err := auditFilter(r)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
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
		var total int
		if err := s.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM audit_logs a WHERE `+whereSQL, args...).Scan(&total); err != nil {
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
		listArgs := append(append([]any{}, args...), size, (page-1)*size)
		rows, err := s.DB.QueryContext(r.Context(), fmt.Sprintf(`
			SELECT a.id, COALESCE(a.user_id,''), COALESCE(a.username,''), COALESCE(a.action,''),
			       COALESCE(a.resource_type,''), COALESCE(a.resource_id,''), COALESCE(a.resource_name,''),
			       COALESCE(a.ip_address,''), COALESCE(a.status,''), COALESCE(a.error_message,''),
			       COALESCE(a.details,'{}'::jsonb), a.created_at
			  FROM audit_logs a
			 WHERE %s
			 ORDER BY a.created_at DESC, a.id DESC
			 LIMIT $%d OFFSET $%d`, whereSQL, len(listArgs)-1, len(listArgs)), listArgs...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()
		list := make([]map[string]any, 0, size)
		for rows.Next() {
			var (
				id                                                int64
				userID, username, action, resType, resID, resName string
				ip, status, errMsg                                string
				details                                           []byte
				createdAt                                         time.Time
			)
			if err := rows.Scan(&id, &userID, &username, &action, &resType, &resID, &resName,
				&ip, &status, &errMsg, &details, &createdAt); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			if len(details) == 0 {
				details = []byte("{}")
			}
			list = append(list, map[string]any{
				"id": id, "user_id": userID, "username": username, "action": action,
				"resource_type": resType, "resource_id": resID, "resource_name": resName,
				"ip_address": ip, "status": status, "error_message": errMsg,
				"details": json.RawMessage(details), "created_at": createdAt,
			})
		}
		if err := rows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{
			"list": list, "total": total, "page": page, "size": size, "pages": pages,
		})
	}
}

// AuditLogExport 导出审计日志 CSV（管理员，最多 20000 行）。
//
// 使用浏览器直接下载（会话 Cookie 鉴权），UTF-8 BOM 便于 Excel 正确识别中文。
func AuditLogExport(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		whereSQL, args, err := auditFilter(r)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT COALESCE(a.username,''), COALESCE(a.action,''), COALESCE(a.resource_type,''),
			       COALESCE(a.resource_id,''), COALESCE(a.resource_name,''), COALESCE(a.status,''),
			       COALESCE(a.ip_address,''), COALESCE(a.error_message,''),
			       COALESCE(a.details,'{}'::jsonb), a.created_at
			  FROM audit_logs a
			 WHERE `+whereSQL+`
			 ORDER BY a.created_at DESC, a.id DESC
			 LIMIT 20000`, args...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()

		filename := "audit_logs_" + time.Now().Format("20060102_150405") + ".csv"
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"时间", "用户", "操作", "资源类型", "资源ID", "资源名称", "状态", "IP", "错误", "详情"})
		for rows.Next() {
			var (
				username, action, resType, resID, resName, status, ip, errMsg string
				details                                                       []byte
				createdAt                                                     time.Time
			)
			if err := rows.Scan(&username, &action, &resType, &resID, &resName, &status,
				&ip, &errMsg, &details, &createdAt); err != nil {
				continue
			}
			_ = cw.Write([]string{
				createdAt.Local().Format("2006-01-02 15:04:05"),
				username, action, resType, resID, resName, status, ip, errMsg, string(details),
			})
		}
		cw.Flush()
		auditOp(s, r, "audit.export", "audit_log", "", "", nil)
	}
}
