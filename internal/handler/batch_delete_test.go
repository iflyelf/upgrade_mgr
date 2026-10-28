package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/artifact"
)

type batchDeleteResp struct {
	Data struct {
		Deleted []int64 `json:"deleted"`
		Failed  []struct {
			ID    int64  `json:"id"`
			Error string `json:"error"`
		} `json:"failed"`
	} `json:"data"`
}

func callBatchDelete(t *testing.T, h http.HandlerFunc, path string, ids []int64, u *auth.User) (int, batchDeleteResp) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"ids": ids})
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), u))
	rec := httptest.NewRecorder()
	h(rec, req)
	var out batchDeleteResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestHostBatchDelete 批量删除主机：可删的删除，运行中任务的主机与跨租户主机记为失败。
func TestHostBatchDelete(t *testing.T) {
	db, sc := setupHostDB(t)
	a, _ := insertTestHost(t, db, 1)
	b, _ := insertTestHost(t, db, 1)
	busy, _ := insertTestHost(t, db, 1)
	other, _ := insertTestHost(t, db, 1)
	if _, err := db.Exec(`UPDATE hosts SET tenant_id=9903 WHERE id=$1`, other); err != nil {
		// 租户 9903 可能不存在：外键失败时改用不存在的 ID 模拟「无权访问」。
		other = 1 << 40
	}

	sfx := time.Now().UnixNano()
	var taskID int64
	if err := db.QueryRow(`
		INSERT INTO upgrade_tasks (tenant_id, name, kind, version, status)
		VALUES (1, $1, 'command', 'v1', 'running') RETURNING id`,
		fmt.Sprintf("host-batch-%d", sfx)).Scan(&taskID); err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID) })
	if _, err := db.Exec(`INSERT INTO task_hosts (task_id, host_id, status) VALUES ($1,$2,'running')`,
		taskID, busy); err != nil {
		t.Fatalf("插入 task_host 失败: %v", err)
	}

	code, out := callBatchDelete(t, HostBatchDelete(sc), "/api/hosts/batch-delete",
		[]int64{a, b, busy, other, a}, hostTestUser(1))
	if code != http.StatusOK {
		t.Fatalf("批量删除应 200，实际 %d", code)
	}
	if len(out.Data.Deleted) != 2 || len(out.Data.Failed) != 2 {
		t.Fatalf("结果不符: %+v", out.Data)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE id = ANY(ARRAY[$1,$2]::bigint[])`, a, b).Scan(&n)
	if n != 0 {
		t.Fatalf("可删除主机未删除，剩余 %d", n)
	}
	db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE id=$1`, busy).Scan(&n)
	if n != 1 {
		t.Fatal("有运行中任务的主机不应被删除")
	}

	if code, _ := callBatchDelete(t, HostBatchDelete(sc), "/api/hosts/batch-delete", nil, hostTestUser(1)); code != http.StatusBadRequest {
		t.Fatalf("空列表应 400，实际 %d", code)
	}
}

// TestArtifactDelete 单个/批量删除制品：文件与记录一并删除，被运行中任务使用的制品拒绝删除。
func TestArtifactDelete(t *testing.T) {
	db, sc := setupHostDB(t)
	root := t.TempDir()
	sc.Artifact = artifact.NewRepo(db, root)
	sfx := time.Now().UnixNano()

	var compID int64
	if err := db.QueryRow(`INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING id`,
		fmt.Sprintf("artdel-%d", sfx)).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM components WHERE id=$1`, compID) })

	newArt := func(version string) (int64, string) {
		rel := fmt.Sprintf("%d/%s-amd64/app.bin", compID, version)
		abs := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(abs), 0o755)
		os.WriteFile(abs, []byte("x"), 0o644)
		var id int64
		if err := db.QueryRow(`
			INSERT INTO artifacts (tenant_id, component_id, version, os_family, arch,
			                       filename, size, md5, sha256, storage, path, status)
			VALUES (1,$1,$2,'','amd64','app.bin',1,'m','s','local',$3,'active') RETURNING id`,
			compID, version, rel).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id, abs
	}
	single, singleFile := newArt("v1")
	b1, b1File := newArt("v2")
	inUse, inUseFile := newArt("v3")

	var taskID int64
	if err := db.QueryRow(`
		INSERT INTO upgrade_tasks (tenant_id, name, component_id, version, status)
		VALUES (1,$1,$2,'v3','paused') RETURNING id`, fmt.Sprintf("artdel-task-%d", sfx), compID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID) })

	// 单个删除
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/artifacts/%d", single), nil)
	req.SetPathValue("id", fmt.Sprint(single))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	ArtifactDelete(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("单个删除应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(singleFile); !os.IsNotExist(err) {
		t.Fatal("制品文件未删除")
	}

	// 跨租户不可删除
	code, out := callBatchDelete(t, ArtifactBatchDelete(sc), "/api/artifacts/batch-delete", []int64{b1}, hostTestUser(9904))
	if code != http.StatusOK || len(out.Data.Failed) != 1 {
		t.Fatalf("跨租户应记为失败: %d %+v", code, out.Data)
	}

	// 批量：一个可删，一个被已暂停任务使用
	code, out = callBatchDelete(t, ArtifactBatchDelete(sc), "/api/artifacts/batch-delete", []int64{b1, inUse}, hostTestUser(1))
	if code != http.StatusOK || len(out.Data.Deleted) != 1 || out.Data.Deleted[0] != b1 ||
		len(out.Data.Failed) != 1 || out.Data.Failed[0].ID != inUse {
		t.Fatalf("批量删除结果不符: %d %+v", code, out.Data)
	}
	if _, err := os.Stat(b1File); !os.IsNotExist(err) {
		t.Fatal("批量删除的制品文件未删除")
	}
	if _, err := os.Stat(inUseFile); err != nil {
		t.Fatal("使用中的制品文件不应被删除")
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE id=$1`, inUse).Scan(&n)
	if n != 1 {
		t.Fatal("使用中的制品记录不应被删除")
	}
}
