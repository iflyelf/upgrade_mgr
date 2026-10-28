package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// TestTaskDeletableIDs 按筛选条件返回可删除任务：只含草稿/终态，且遵循列表筛选。
func TestTaskDeletableIDs(t *testing.T) {
	db, sc := setupHostDB(t)
	sfx := time.Now().UnixNano()
	q := fmt.Sprintf("deletable_ids_%d", sfx)

	newTask := func(status string) int64 {
		var id int64
		if err := db.QueryRow(`
			INSERT INTO upgrade_tasks (name, version, kind, status, created_by, updated_by, tenant_id)
			VALUES ($1,'v1','command',$2,'tester','tester',1) RETURNING id`,
			q+"_"+status, status).Scan(&id); err != nil {
			t.Fatalf("建任务失败: %v", err)
		}
		t.Cleanup(func() { db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, id) })
		return id
	}
	draft := newTask("draft")
	failed := newTask("failed")
	running := newTask("running")

	call := func(query string) map[int64]bool {
		req := httptest.NewRequest("GET", "/api/tasks/deletable-ids?"+query, nil)
		req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
		rec := httptest.NewRecorder()
		TaskDeletableIDs(sc)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("请求失败 %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data struct {
				IDs       []int64 `json:"ids"`
				Truncated bool    `json:"truncated"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		got := map[int64]bool{}
		for _, id := range out.Data.IDs {
			got[id] = true
		}
		return got
	}

	got := call("q=" + q)
	if !got[draft] || !got[failed] || got[running] {
		t.Fatalf("应只返回草稿/终态任务，got=%v", got)
	}
	// 叠加状态筛选：只剩 failed。
	got = call("q=" + q + "&status=failed")
	if got[draft] || !got[failed] || got[running] {
		t.Fatalf("状态筛选未生效，got=%v", got)
	}
}
