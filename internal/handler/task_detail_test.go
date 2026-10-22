package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/task"
)

// taskDetailResp 只声明测试关心的字段。
type taskDetailResp struct {
	Code int `json:"code"`
	Data struct {
		ID        int64               `json:"id"`
		Total     int                 `json:"total"`
		Success   int                 `json:"success"`
		Failed    int                 `json:"failed"`
		Done      int                 `json:"done"`
		Active    int                 `json:"active"`
		HostTotal int                 `json:"host_total"`
		HostPage  int                 `json:"host_page"`
		HostSize  int                 `json:"host_size"`
		HostPages int                 `json:"host_pages"`
		Batches   []taskBatchDetail   `json:"batches"`
		Hosts     []taskHostDetail    `json:"hosts"`
		Commands  []taskCommandDetail `json:"commands"`
	} `json:"data"`
}

type taskFixture struct {
	taskID   int64
	hostIDs  []int64
	thIDs    []int64
	compID   int64
	hostPref string
}

// insertTaskFixture 创建任务、批次、主机与逐机记录，供详情/删除测试复用。
func insertTaskFixture(t *testing.T, db *sql.DB, tenantID int64, status string, hostN int) taskFixture {
	t.Helper()
	sfx := time.Now().UnixNano()
	f := taskFixture{hostPref: fmt.Sprintf("task-fixture-%d", sfx)}

	if err := db.QueryRow(`
		INSERT INTO components (tenant_id, name, display_name, deploy_mode)
		VALUES ($1, $2, '测试组件', 'replace') RETURNING id`,
		tenantID, fmt.Sprintf("task_comp_%d", sfx)).Scan(&f.compID); err != nil {
		t.Fatalf("插入组件失败: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO upgrade_tasks (tenant_id, name, component_id, version, status, created_by)
		VALUES ($1, $2, $3, '2.0.0', $4, 'tester') RETURNING id`,
		tenantID, fmt.Sprintf("task_%d", sfx), f.compID, status).Scan(&f.taskID); err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO task_batches (task_id, batch_index, status, total)
		VALUES ($1, 0, 'running', $2)`, f.taskID, hostN); err != nil {
		t.Fatalf("插入批次失败: %v", err)
	}

	hostStatuses := []string{"success", "running", "failed"}
	for i := 0; i < hostN; i++ {
		ip := fmt.Sprintf("%s-%d", f.hostPref, i)
		var hostID int64
		if err := db.QueryRow(`
			INSERT INTO hosts (ip, hostname, tenant_id, source, agent_id)
			VALUES ($1, $2, $3, 'agent', $4) RETURNING id`,
			ip, fmt.Sprintf("host-%d", i), tenantID, fmt.Sprintf("agent-%d-%d", sfx, i)).Scan(&hostID); err != nil {
			t.Fatalf("插入主机失败: %v", err)
		}
		f.hostIDs = append(f.hostIDs, hostID)

		hs := "pending"
		if i < len(hostStatuses) {
			hs = hostStatuses[i]
		}
		stage := "downloading"
		var thID int64
		if err := db.QueryRow(`
			INSERT INTO task_hosts
				(task_id, host_id, batch_index, status, stage, from_version, to_version, attempts, log)
			VALUES ($1, $2, 0, $3, $4, '1.0.0', '2.0.0', 1, $5)
			RETURNING id`,
			f.taskID, hostID, hs, stage, fmt.Sprintf("host-final-log-%d", i)).Scan(&thID); err != nil {
			t.Fatalf("插入逐机记录失败: %v", err)
		}
		f.thIDs = append(f.thIDs, thID)
	}

	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, f.taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, f.compID)
		db.Exec(`DELETE FROM hosts WHERE ip LIKE $1`, f.hostPref+"%")
	})
	return f
}

func TestTaskDetailPaginationAndLogs(t *testing.T) {
	db, sc := setupHostDB(t)
	f := insertTaskFixture(t, db, 1, "running", 3)

	// 每台主机一条指令，便于验证详情只返回当前页主机的日志。
	for i, thID := range f.thIDs {
		if _, err := db.Exec(`
			INSERT INTO commands
				(task_id, task_host_id, host_id, agent_id, type, payload, status, attempts, log)
			VALUES ($1, $2, $3, $4, 'upgrade_component', '{}', 'running', 1, $5)`,
			f.taskID, thID, f.hostIDs[i], fmt.Sprintf("agent-fixture-%d", i),
			fmt.Sprintf("log-for-host-%d", i)); err != nil {
			t.Fatalf("插入指令失败: %v", err)
		}
	}

	req := httptest.NewRequest("GET", fmt.Sprintf("/api/tasks/%d?host_page=2&host_size=2", f.taskID), nil)
	req.SetPathValue("id", strconv.FormatInt(f.taskID, 10))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	TaskDetail(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("任务详情失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out taskDetailResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析详情响应失败: %v", err)
	}
	if out.Data.Total != 3 || out.Data.Success != 1 || out.Data.Failed != 1 || out.Data.Done != 2 || out.Data.Active != 1 {
		t.Fatalf("任务汇总不符: %+v", out.Data)
	}
	if out.Data.HostTotal != 3 || out.Data.HostPage != 2 || out.Data.HostSize != 2 || out.Data.HostPages != 2 {
		t.Fatalf("分页信息不符: %+v", out.Data)
	}
	if len(out.Data.Hosts) != 1 {
		t.Fatalf("第 2 页应只有 1 台主机，实际 %d", len(out.Data.Hosts))
	}
	if len(out.Data.Commands) != 1 || out.Data.Commands[0].Log != "log-for-host-2" {
		t.Fatalf("应只返回当前页主机的指令日志: %+v", out.Data.Commands)
	}
	if len(out.Data.Batches) != 1 || out.Data.Batches[0].Done != 2 {
		t.Fatalf("批次进度不符: %+v", out.Data.Batches)
	}

	// 跨租户读取必须 404，不能泄露任务存在性。
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/tasks/%d", f.taskID), nil)
	req.SetPathValue("id", strconv.FormatInt(f.taskID, 10))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(9902)))
	rec = httptest.NewRecorder()
	TaskDetail(sc)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨租户读取应 404，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTaskDeleteRulesAndCascade(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Task = task.NewService(db, nil, nil, nil)
	f := insertTaskFixture(t, db, 1, "running", 1)

	var activeCmd int64
	if err := db.QueryRow(`
		INSERT INTO commands
			(task_id, task_host_id, host_id, agent_id, type, payload, status)
		VALUES ($1, $2, $3, 'agent-delete', 'upgrade_component', '{}', 'pending')
		RETURNING id`, f.taskID, f.thIDs[0], f.hostIDs[0]).Scan(&activeCmd); err != nil {
		t.Fatalf("插入活动指令失败: %v", err)
	}

	del := func(id int64, u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/tasks/%d", id), nil)
		req.SetPathValue("id", strconv.FormatInt(id, 10))
		req = req.WithContext(auth.WithUser(req.Context(), u))
		rec := httptest.NewRecorder()
		TaskDelete(sc)(rec, req)
		return rec
	}
	taskExists := func() bool {
		var n int
		db.QueryRow(`SELECT COUNT(1) FROM upgrade_tasks WHERE id=$1`, f.taskID).Scan(&n)
		return n == 1
	}

	// running 不允许删除。
	if rec := del(f.taskID, hostTestUser(1)); rec.Code == http.StatusOK {
		t.Fatalf("运行中任务应拒绝删除: %s", rec.Body.String())
	}
	if !taskExists() {
		t.Fatal("运行中任务不应被删除")
	}

	// 取消后仍有活动指令，仍应拒绝，避免删除正在执行的升级。
	if _, err := db.Exec(`UPDATE upgrade_tasks SET status='canceled' WHERE id=$1`, f.taskID); err != nil {
		t.Fatal(err)
	}
	if rec := del(f.taskID, hostTestUser(1)); rec.Code == http.StatusOK {
		t.Fatalf("存在活动指令时应拒绝删除: %s", rec.Body.String())
	}
	if !taskExists() {
		t.Fatal("存在活动指令时任务不应被删除")
	}

	// 指令结束后，终态任务可删除，并级联清理批次/逐机/指令。
	if _, err := db.Exec(`UPDATE commands SET status='failed' WHERE id=$1`, activeCmd); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE upgrade_tasks SET status='success' WHERE id=$1`, f.taskID); err != nil {
		t.Fatal(err)
	}
	if rec := del(f.taskID, hostTestUser(9902)); rec.Code == http.StatusOK ||
		!strings.Contains(rec.Body.String(), "任务不存在") {
		t.Fatalf("跨租户删除应提示任务不存在且不删除，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if !taskExists() {
		t.Fatal("跨租户删除不应删除任务")
	}
	if rec := del(f.taskID, hostTestUser(1)); rec.Code != http.StatusOK {
		t.Fatalf("终态任务应可删除，实际 %d: %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{
		`SELECT COUNT(1) FROM upgrade_tasks WHERE id=$1`,
		`SELECT COUNT(1) FROM task_batches WHERE task_id=$1`,
		`SELECT COUNT(1) FROM task_hosts WHERE task_id=$1`,
		`SELECT COUNT(1) FROM commands WHERE task_id=$1`,
	} {
		var n int
		if err := db.QueryRow(q, f.taskID).Scan(&n); err != nil {
			t.Fatalf("级联删除校验失败: %v", err)
		}
		if n != 0 {
			t.Fatalf("删除后仍有残留数据: %s => %d", q, n)
		}
	}

	// draft 任务无需取消即可删除。
	d := insertTaskFixture(t, db, 1, "draft", 0)
	if rec := del(d.taskID, hostTestUser(1)); rec.Code != http.StatusOK {
		t.Fatalf("draft 任务应可删除，实际 %d: %s", rec.Code, rec.Body.String())
	}
}
