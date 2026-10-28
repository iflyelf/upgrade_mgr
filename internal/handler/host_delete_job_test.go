package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/artifact"
	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
)

type hostJobResp struct {
	Data *hostDeleteJob `json:"data"`
}

func callHostJob(t *testing.T, h http.HandlerFunc, method, path, id, body string, u *auth.User) (int, *hostDeleteJob) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if id != "" {
		req.SetPathValue("id", id)
	}
	req = req.WithContext(auth.WithUser(req.Context(), u))
	rec := httptest.NewRecorder()
	h(rec, req)
	var out hostJobResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.Data
}

// insertBusyHost 插入一台有运行中任务的主机（删除应失败）。
func insertBusyHost(t *testing.T, db *sql.DB, sfx string) int64 {
	t.Helper()
	busy, _ := insertTestHost(t, db, 1)
	var taskID int64
	if err := db.QueryRow(`
		INSERT INTO upgrade_tasks (tenant_id, name, kind, version, status)
		VALUES (1, $1, 'command', 'v1', 'running') RETURNING id`, "hostjob-"+sfx).Scan(&taskID); err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID) })
	if _, err := db.Exec(`INSERT INTO task_hosts (task_id, host_id, status) VALUES ($1,$2,'running')`,
		taskID, busy); err != nil {
		t.Fatalf("插入 task_host 失败: %v", err)
	}
	return busy
}

// TestHostDeletableIDs 按筛选条件全选：与列表同一筛选，排除有未完成任务的主机。
func TestHostDeletableIDs(t *testing.T) {
	db, sc := setupHostDB(t)
	tag := fmt.Sprintf("dsel-%d", time.Now().UnixNano())
	a, _ := insertTestHost(t, db, 1)
	b, _ := insertTestHost(t, db, 1)
	busy := insertBusyHost(t, db, tag)
	other, _ := insertTestHost(t, db, 1) // 不匹配筛选条件
	if _, err := db.Exec(`UPDATE hosts SET hostname=$1 WHERE id = ANY(ARRAY[$2,$3,$4]::bigint[])`,
		tag, a, b, busy); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/hosts/deletable-ids?q="+tag, nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	HostDeletableIDs(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			IDs       []int64 `json:"ids"`
			Truncated bool    `json:"truncated"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	got := map[int64]bool{}
	for _, id := range out.Data.IDs {
		got[id] = true
	}
	if len(out.Data.IDs) != 2 || !got[a] || !got[b] || got[busy] || got[other] || out.Data.Truncated {
		t.Fatalf("结果不符: %+v（a=%d b=%d busy=%d other=%d）", out.Data, a, b, busy, other)
	}

	// 其他租户看不到
	req = httptest.NewRequest(http.MethodGet, "/api/hosts/deletable-ids?q="+tag, nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(9905)))
	rec = httptest.NewRecorder()
	HostDeletableIDs(sc)(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Data.IDs) != 0 {
		t.Fatalf("跨租户不应返回主机: %+v", out.Data.IDs)
	}
}

// TestHostDeleteJobLifecycle 异步主机删除作业：创建 → 后台执行 → 完成；
// 有运行中任务的主机记为失败；同一发起人同时只允许一个作业；停止后剩余不处理。
func TestHostDeleteJobLifecycle(t *testing.T) {
	db, sc := setupHostDB(t)
	ctx := context.Background()
	u := hostTestUser(1)
	sfx := fmt.Sprint(time.Now().UnixNano())
	t.Cleanup(func() { db.Exec(`DELETE FROM host_delete_jobs WHERE created_by=$1`, u.Username) })
	db.Exec(`DELETE FROM host_delete_jobs WHERE created_by=$1`, u.Username)

	a, _ := insertTestHost(t, db, 1)
	b, _ := insertTestHost(t, db, 1)
	busy := insertBusyHost(t, db, sfx)

	body := fmt.Sprintf(`{"ids":[%d,%d,%d,%d]}`, a, b, busy, a)
	code, job := callHostJob(t, HostDeleteJobCreate(sc), http.MethodPost, "/api/host-delete-jobs", "", body, u)
	if code != http.StatusOK || job == nil || job.Total != 3 || job.Status != "pending" {
		t.Fatalf("创建作业失败: %d %+v", code, job)
	}
	// 同一发起人第二个作业被拒绝
	if code, _ := callHostJob(t, HostDeleteJobCreate(sc), http.MethodPost, "/api/host-delete-jobs", "",
		fmt.Sprintf(`{"ids":[%d]}`, a), u); code != http.StatusConflict {
		t.Fatalf("重复作业应 409，实际 %d", code)
	}
	// 页面刷新后能查到进行中的作业
	if _, active := callHostJob(t, HostDeleteJobActive(sc), http.MethodGet, "/api/host-delete-jobs/active", "", "", u); active == nil || active.ID != job.ID {
		t.Fatalf("active 应返回作业 #%d: %+v", job.ID, active)
	}

	for i := 0; i < 10; i++ {
		did, err := runHostDeleteJobOnce(ctx, sc)
		if err != nil {
			t.Fatalf("执行作业失败: %v", err)
		}
		if !did {
			break
		}
	}
	_, done := callHostJob(t, HostDeleteJobGet(sc), http.MethodGet, "/api/host-delete-jobs/x", fmt.Sprint(job.ID), "", u)
	if done == nil || done.Status != "done" || done.Processed != 3 || done.Deleted != 2 ||
		done.FailedCount != 1 || len(done.Failed) != 1 || done.Failed[0].ID != busy || done.FinishedAt == nil {
		t.Fatalf("作业结果不符: %+v", done)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE id = ANY(ARRAY[$1,$2,$3]::bigint[])`, a, b, busy).Scan(&n)
	if n != 1 {
		t.Fatalf("应只剩运行中任务的主机，实际剩余 %d", n)
	}
	if _, active := callHostJob(t, HostDeleteJobActive(sc), http.MethodGet, "/api/host-delete-jobs/active", "", "", u); active != nil {
		t.Fatalf("作业结束后 active 应为空: %+v", active)
	}
	// 其他用户看不到该作业
	stranger := hostTestUser(1)
	stranger.Username = "stranger"
	if code, _ := callHostJob(t, HostDeleteJobGet(sc), http.MethodGet, "/api/host-delete-jobs/x", fmt.Sprint(job.ID), "", stranger); code != http.StatusNotFound {
		t.Fatalf("他人作业应 404，实际 %d", code)
	}

	// 停止：未执行前请求停止，剩余主机不处理。
	c, _ := insertTestHost(t, db, 1)
	_, job2 := callHostJob(t, HostDeleteJobCreate(sc), http.MethodPost, "/api/host-delete-jobs", "",
		fmt.Sprintf(`{"ids":[%d]}`, c), u)
	if job2 == nil {
		t.Fatal("创建第二个作业失败")
	}
	if _, j := callHostJob(t, HostDeleteJobCancel(sc), http.MethodPost, "/api/host-delete-jobs/x/cancel", fmt.Sprint(job2.ID), "", u); j == nil || !j.CancelRequested {
		t.Fatalf("停止请求未记录: %+v", j)
	}
	if _, err := runHostDeleteJobOnce(ctx, sc); err != nil {
		t.Fatal(err)
	}
	_, canceled := callHostJob(t, HostDeleteJobGet(sc), http.MethodGet, "/api/host-delete-jobs/x", fmt.Sprint(job2.ID), "", u)
	if canceled == nil || canceled.Status != "canceled" || canceled.Deleted != 0 {
		t.Fatalf("停止后的作业状态不符: %+v", canceled)
	}
	db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE id=$1`, c).Scan(&n)
	if n != 1 {
		t.Fatal("已停止作业不应删除主机")
	}

	if code, _ := callHostJob(t, HostDeleteJobCreate(sc), http.MethodPost, "/api/host-delete-jobs", "", `{"ids":[]}`, u); code != http.StatusBadRequest {
		t.Fatalf("空列表应 400，实际 %d", code)
	}
}

// TestComponentBatchDelete 批量删除组件：未被引用的删除，被任务引用的记为失败（不连带删除任务）。
func TestComponentBatchDelete(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Component = component.NewRepo(db)
	sc.Artifact = artifact.NewRepo(db, t.TempDir())
	sfx := time.Now().UnixNano()

	newComp := func(name string) int64 {
		var id int64
		if err := db.QueryRow(`INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING id`,
			fmt.Sprintf("%s-%d", name, sfx)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec(`DELETE FROM components WHERE id=$1`, id) })
		return id
	}
	free := newComp("cbd-free")
	used := newComp("cbd-used")
	var taskID int64
	if err := db.QueryRow(`
		INSERT INTO upgrade_tasks (tenant_id, name, component_id, version, status)
		VALUES (1,$1,$2,'v1','success') RETURNING id`, fmt.Sprintf("cbd-task-%d", sfx), used).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID) })

	code, out := callBatchDelete(t, ComponentBatchDelete(sc), "/api/components/batch-delete",
		[]int64{free, used, free}, hostTestUser(1))
	if code != http.StatusOK || len(out.Data.Deleted) != 1 || out.Data.Deleted[0] != free ||
		len(out.Data.Failed) != 1 || out.Data.Failed[0].ID != used {
		t.Fatalf("批量删除结果不符: %d %+v", code, out.Data)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM components WHERE id=$1`, used).Scan(&n)
	if n != 1 {
		t.Fatal("被任务引用的组件不应删除")
	}
	db.QueryRow(`SELECT COUNT(*) FROM upgrade_tasks WHERE id=$1`, taskID).Scan(&n)
	if n != 1 {
		t.Fatal("批量删除不应连带删除任务")
	}
	if code, _ := callBatchDelete(t, ComponentBatchDelete(sc), "/api/components/batch-delete", nil, hostTestUser(1)); code != http.StatusBadRequest {
		t.Fatalf("空列表应 400，实际 %d", code)
	}
}
