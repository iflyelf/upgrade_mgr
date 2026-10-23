package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestSyncDepartmentsFromFlyIAM 从 FlyIAM 拉取组织树并整体落库。
func TestSyncDepartmentsFromFlyIAM(t *testing.T) {
	db, _ := setupHostDB(t)
	sfx := time.Now().UnixNano()
	root := fmt.Sprintf("dept-root-%d", sfx)
	child := fmt.Sprintf("dept-child-%d", sfx)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/departments/export" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": []map[string]any{
				{"deptId": root, "deptName": "集团", "level": 0, "fullPath": "集团"},
				{"deptId": child, "deptName": "研发", "parentId": root,
					"level": 1, "fullPath": "集团/研发", "userCount": 8},
			},
		})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { db.Exec(`DELETE FROM org_departments`) })

	sc := &svc.ServiceContext{
		DB: db,
		Config: &config.Config{FlyIAM: config.FlyIAMConfig{
			Endpoint: srv.URL, ServiceToken: "tok",
		}},
	}

	req := httptest.NewRequest("POST", "/api/flyiam/sync-departments", nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	SyncDepartmentsFromFlyIAM(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("同步失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Total int `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Total != 2 {
		t.Fatalf("total=%d", out.Data.Total)
	}

	req = httptest.NewRequest("GET", "/api/org/departments", nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec = httptest.NewRecorder()
	DepartmentList(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表失败 %d: %s", rec.Code, rec.Body.String())
	}
	var listOut struct {
		Data []struct {
			DeptID    string `json:"dept_id"`
			ParentID  string `json:"parent_id"`
			UserCount int    `json:"user_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listOut); err != nil {
		t.Fatal(err)
	}
	if len(listOut.Data) != 2 {
		t.Fatalf("应有 2 个部门: %+v", listOut.Data)
	}
	if listOut.Data[0].DeptID != root || listOut.Data[1].DeptID != child ||
		listOut.Data[1].ParentID != root || listOut.Data[1].UserCount != 8 {
		t.Fatalf("组织数据不符: %+v", listOut.Data)
	}
}

// TestSyncDepartmentsEmptyGuard 上游返回空组织时保留本地快照（防误清空）。
func TestSyncDepartmentsEmptyGuard(t *testing.T) {
	db, _ := setupHostDB(t)
	sfx := time.Now().UnixNano()
	keep := fmt.Sprintf("dept-keep-%d", sfx)
	if _, err := db.Exec(`
		INSERT INTO org_departments (dept_id, dept_name, level, full_path)
		VALUES ($1,'保留部门',0,'保留部门')`, keep); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM org_departments`) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": []any{}})
	}))
	t.Cleanup(srv.Close)

	sc := &svc.ServiceContext{
		DB: db,
		Config: &config.Config{FlyIAM: config.FlyIAMConfig{
			Endpoint: srv.URL, ServiceToken: "tok",
		}},
	}
	req := httptest.NewRequest("POST", "/api/flyiam/sync-departments", nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	SyncDepartmentsFromFlyIAM(sc)(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("空组织应 502，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM org_departments WHERE dept_id=$1)`, keep).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("本地快照不应被清空")
	}
}
