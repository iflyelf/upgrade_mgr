package handler

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// TestHostBatchUpdate 批量编辑内置字段与自定义字段。
func TestHostBatchUpdate(t *testing.T) {
	db, sc := setupHostDB(t)
	prefix := fmt.Sprintf("bulk-%d", time.Now().UnixNano())
	customKey := "rack_" + strings.ReplaceAll(prefix, "-", "_")
	if _, err := db.Exec(`INSERT INTO host_field_defs
		(field_key, label, field_type, show_in_list, show_in_form, editable, builtin, sort_order)
		VALUES ($1, '机架', 'text', false, true, true, false, 999)`, customKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM host_field_defs WHERE field_key=$1`, customKey) })

	var id1, id2 int64
	for i, ip := range []string{prefix + "-1", prefix + "-2"} {
		var id int64
		if err := db.QueryRow(`INSERT INTO hosts (ip, hostname, tenant_id, source)
			VALUES ($1, $2, 1, 'manual') RETURNING id`, ip, fmt.Sprintf("old-%d", i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			id1 = id
		} else {
			id2 = id
		}
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE ip LIKE $1`, prefix+"%") })

	body, _ := json.Marshal(map[string]any{
		"ids":           []int64{id1, id2},
		"fields":        map[string]string{"hostname": "new-host", "app_code": "PAY"},
		"custom_fields": map[string]string{customKey: "R01"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hosts/batch-update", strings.NewReader(string(body)))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	HostBatchUpdate(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("批量编辑失败 %d: %s", rec.Code, rec.Body.String())
	}
	for _, id := range []int64{id1, id2} {
		var hostname, appCode, custom string
		if err := db.QueryRow(`SELECT hostname, app_code, COALESCE(custom_fields->>$2,'')
			FROM hosts WHERE id=$1`, id, customKey).Scan(&hostname, &appCode, &custom); err != nil {
			t.Fatal(err)
		}
		if hostname != "new-host" || appCode != "PAY" || custom != "R01" {
			t.Fatalf("主机 %d 未更新: hostname=%q app=%q custom=%q", id, hostname, appCode, custom)
		}
	}
}

// TestHostImportCSV 验证按 ip 更新已有主机、创建缺失主机。
func TestHostImportCSV(t *testing.T) {
	db, sc := setupHostDB(t)
	prefix := fmt.Sprintf("csv-%d", time.Now().UnixNano())
	existingIP := prefix + "-existing"
	newIP := prefix + "-new"
	if _, err := db.Exec(`INSERT INTO hosts (ip, hostname, tenant_id, source)
		VALUES ($1, 'old', 1, 'manual')`, existingIP); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE ip LIKE $1`, prefix+"%") })

	csv := "ip,hostname,app_code\n" + existingIP + ",updated,PAY\n" + newIP + ",created,ORDER\n"
	body, _ := json.Marshal(map[string]any{"csv": csv, "create_missing": true})
	req := httptest.NewRequest(http.MethodPost, "/api/hosts/import", strings.NewReader(string(body)))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	HostImportCSV(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("导入失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Created int `json:"created"`
			Updated int `json:"updated"`
			Failed  int `json:"failed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Created != 1 || out.Data.Updated != 1 || out.Data.Failed != 0 {
		t.Fatalf("导入统计不符: %+v", out.Data)
	}
	var hostname, appCode, source string
	if err := db.QueryRow(`SELECT hostname, app_code, source FROM hosts WHERE ip=$1`, existingIP).
		Scan(&hostname, &appCode, &source); err != nil {
		t.Fatal(err)
	}
	if hostname != "updated" || appCode != "PAY" || source != "manual" {
		t.Fatalf("已有主机未更新: %q %q %q", hostname, appCode, source)
	}
	if err := db.QueryRow(`SELECT hostname, source FROM hosts WHERE ip=$1`, newIP).
		Scan(&hostname, &source); err != nil {
		t.Fatal(err)
	}
	if hostname != "created" || source != "manual" {
		t.Fatalf("新主机未创建: %q %q", hostname, source)
	}
}

// TestAutomationTokenBatchDelete 批量删除自动化令牌并按租户隔离。
func TestAutomationTokenBatchDelete(t *testing.T) {
	db, sc := setupHostDB(t)
	tenantID := int64(900000 + time.Now().UnixNano()%100000)
	if _, err := db.Exec(`INSERT INTO tenants (id, name, display_name, status)
		VALUES ($1, $2, '测试租户', 'active')`, tenantID, fmt.Sprintf("tok-tenant-%d", tenantID)); err != nil {
		t.Fatal(err)
	}
	// 先注册租户清理，后注册令牌清理：t.Cleanup 为 LIFO，令牌先删再删租户。
	t.Cleanup(func() { db.Exec(`DELETE FROM tenants WHERE id=$1`, tenantID) })
	prefix := fmt.Sprintf("tok-%d", time.Now().UnixNano())
	insert := func(name string, tenant int64) int64 {
		var id int64
		if err := db.QueryRow(`INSERT INTO enroll_tokens
			(token_hash, token_type, tenant_id, name, token_prefix, expires_at, enabled)
			VALUES ($1, 'automation', $2, $3, 'enr_auto_', NOW() + INTERVAL '1 day', true)
			RETURNING id`, prefix+"-"+name, tenant, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	id1 := insert("one", 1)
	id2 := insert("two", tenantID)
	t.Cleanup(func() { db.Exec(`DELETE FROM enroll_tokens WHERE token_hash LIKE $1`, prefix+"%") })

	body, _ := json.Marshal(map[string]any{"ids": []int64{id1, id2}})
	req := httptest.NewRequest(http.MethodPost, "/api/agent/enroll-tokens/batch-delete", strings.NewReader(string(body)))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	AutomationEnrollTokenBatchDelete(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("批量删除失败 %d: %s", rec.Code, rec.Body.String())
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM enroll_tokens WHERE id=$1`, id1).Scan(&n)
	if n != 0 {
		t.Fatal("本租户令牌应被删除")
	}
	db.QueryRow(`SELECT COUNT(*) FROM enroll_tokens WHERE id=$1`, id2).Scan(&n)
	if n != 1 {
		t.Fatal("其他租户令牌不应被删除")
	}
}

// TestHostExportCSVIncludesOSColumns 导出 CSV 必须包含 os_family/os_type/os_version。
func TestHostExportCSVIncludesOSColumns(t *testing.T) {
	db, sc := setupHostDB(t)
	ip := fmt.Sprintf("198.39.%d.%d", time.Now().UnixNano()%250, time.Now().UnixNano()%250)
	if _, err := db.Exec(`INSERT INTO hosts
		(ip, hostname, os_family, os_type, os_version, tenant_id, source)
		VALUES ($1,'csv-os','rhel','linux','7.9',1,'manual')`, ip); err != nil {
		t.Fatalf("插入主机失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE ip=$1`, ip) })

	req := httptest.NewRequest(http.MethodGet, "/api/hosts/export", nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	HostExportCSV(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("导出失败 %d: %s", rec.Code, rec.Body.String())
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(records) < 2 {
		t.Fatalf("解析 CSV 失败: %v", err)
	}
	header := records[0]
	index := map[string]int{}
	for i, h := range header {
		index[h] = i
	}
	for _, key := range []string{"os_family", "os_type", "os_version"} {
		if _, ok := index[key]; !ok {
			t.Fatalf("CSV 缺少 %s 列: %v", key, header)
		}
	}
	for _, row := range records[1:] {
		if row[index["ip"]] != ip {
			continue
		}
		if row[index["os_family"]] != "rhel" || row[index["os_type"]] != "linux" ||
			row[index["os_version"]] != "7.9" {
			t.Fatalf("OS 列值不符: %v", row)
		}
		return
	}
	t.Fatal("CSV 中未找到测试主机")
}
