package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/artifact"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestArtifactListPaged 制品分页与关键字搜索。
func TestArtifactListPaged(t *testing.T) {
	db, _ := setupHostDB(t)
	sc := &svc.ServiceContext{DB: db, Artifact: artifact.NewRepo(db, t.TempDir())}
	sfx := time.Now().UnixNano()
	prefix := fmt.Sprintf("artpage-%d", sfx)

	var compID int64
	if err := db.QueryRow(`
		INSERT INTO components (name, display_name, deploy_mode)
		VALUES ($1,'分页测试','replace') RETURNING id`, prefix).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM components WHERE id=$1`, compID) })

	for i := 0; i < 3; i++ {
		if _, err := db.Exec(`
			INSERT INTO artifacts (tenant_id, component_id, version, os_family, arch,
			                       filename, size, md5, sha256, storage, path, status)
			VALUES (1,$1,$2,'','amd64',$3,10,'m','s','local','/tmp/x','ready')`,
			compID, fmt.Sprintf("v%d", i), fmt.Sprintf("%s-file-%d.bin", prefix, i)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM artifacts WHERE component_id=$1`, compID) })

	req := httptest.NewRequest("GET",
		"/api/artifacts/paged?page=2&size=2&component_id="+fmt.Sprintf("%d", compID), nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	ArtifactListPaged(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("查询失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			List  []map[string]any `json:"list"`
			Total int              `json:"total"`
			Page  int              `json:"page"`
			Pages int              `json:"pages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Total != 3 || out.Data.Pages != 2 || out.Data.Page != 2 || len(out.Data.List) != 1 {
		t.Fatalf("分页不符: %+v", out.Data)
	}

	// 搜索命中 0 条
	req = httptest.NewRequest("GET",
		"/api/artifacts/paged?q="+prefix+"-file-2&component_id="+fmt.Sprintf("%d", compID), nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec = httptest.NewRecorder()
	ArtifactListPaged(sc)(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Total != 1 || len(out.Data.List) != 1 {
		t.Fatalf("搜索结果不符: %+v", out.Data)
	}
}
