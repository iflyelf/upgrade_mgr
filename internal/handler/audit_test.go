package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestAuditLogListFilters 审计日志：租户隔离、过滤、分页与 CSV 导出。
func TestAuditLogListFilters(t *testing.T) {
	db, _ := setupHostDB(t)
	sc := &svc.ServiceContext{DB: db}
	sfx := fmt.Sprintf("%d", time.Now().UnixNano())
	prefix := "audit-test-" + sfx
	actionA := prefix + "-a"
	actionB := prefix + "-b"
	actionC := prefix + "-c"

	insert := func(tenantID int64, action, status string, at time.Time) int64 {
		var id int64
		if err := db.QueryRow(`
			INSERT INTO audit_logs
			    (username, action, resource_type, resource_id, resource_name,
			     ip_address, status, details, created_at, tenant_id)
			VALUES ('tester',$1,'task','1',$1,'10.0.0.1',$2,'{"k":"v"}'::jsonb,$3,$4)
			RETURNING id`, action, status, at, tenantID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	idA := insert(1, actionA, "success", time.Now())
	idB := insert(1, actionB, "failed", time.Now().Add(-10*24*time.Hour))
	idC := insert(2, actionC, "success", time.Now())
	t.Cleanup(func() {
		db.Exec(`DELETE FROM audit_logs WHERE id IN ($1,$2,$3)`, idA, idB, idC)
	})

	list := func(u *auth.User, query string) (int, []map[string]any) {
		req := httptest.NewRequest("GET", "/api/audit-logs?"+query, nil)
		req = req.WithContext(auth.WithUser(req.Context(), u))
		rec := httptest.NewRecorder()
		AuditLogList(sc)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("查询失败 %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data struct {
				List  []map[string]any `json:"list"`
				Total int              `json:"total"`
				Page  int              `json:"page"`
				Size  int              `json:"size"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data.Total, out.Data.List
	}
	admin := &auth.User{Username: "audit-admin", TenantID: 1, IsAdmin: true}
	super := &auth.User{Username: "audit-root", TenantID: 1, Roles: []string{auth.RoleSuperAdmin}}

	// 租户 1 只能看到自己的记录
	total, rows := list(admin, "q="+prefix)
	if total != 2 || len(rows) != 2 {
		t.Fatalf("租户 1 应看到 2 条, total=%d rows=%d", total, len(rows))
	}
	for _, row := range rows {
		if strings.HasSuffix(fmt.Sprint(row["action"]), "-c") {
			t.Fatalf("租户 1 不应看到租户 2 的记录: %+v", row)
		}
	}
	// superadmin 跨租户
	total, _ = list(super, "q="+prefix)
	if total != 3 {
		t.Fatalf("superadmin 应看到 3 条, got %d", total)
	}
	// 状态过滤
	total, _ = list(super, "q="+prefix+"&status=failed")
	if total != 1 {
		t.Fatalf("失败记录应为 1 条, got %d", total)
	}
	// 日期过滤（最近 5 天，排除 10 天前）
	from := time.Now().Add(-5 * 24 * time.Hour).Format("2006-01-02")
	total, _ = list(super, "q="+prefix+"&from="+from)
	if total != 2 {
		t.Fatalf("最近 5 天应为 2 条, got %d", total)
	}
	// 分页
	total, rows = list(super, "q="+prefix+"&page=2&size=1")
	if total != 3 || len(rows) != 1 {
		t.Fatalf("分页不符: total=%d rows=%d", total, len(rows))
	}

	// CSV 导出（租户 1）
	req := httptest.NewRequest("GET", "/api/audit-logs/export?q="+prefix, nil)
	req = req.WithContext(auth.WithUser(req.Context(), admin))
	rec := httptest.NewRecorder()
	AuditLogExport(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("导出失败 %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type=%q", ct)
	}
	body := rec.Body.Bytes()
	if len(body) < 3 || body[0] != 0xEF || body[1] != 0xBB || body[2] != 0xBF {
		t.Fatal("CSV 应带 UTF-8 BOM")
	}
	text := string(body)
	if !strings.Contains(text, actionA) || strings.Contains(text, actionC) {
		t.Fatalf("导出内容租户隔离不符: %s", text)
	}
}
