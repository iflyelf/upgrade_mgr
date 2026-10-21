package task

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// TestBatchAndDispatchAtomic 验证多副本/滚动重启依赖的两个原子操作：
//  1. completeBatch 同时更新批次状态和任务暂停状态；
//  2. dispatchHost 重复调用不会给同一 task_host 写入第二条指令。
//
// 需要 DATABASE_URL；未设置时跳过。
func TestBatchAndDispatchAtomic(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过数据库集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("数据库迁移失败: %v", err)
	}
	ctx := context.Background()
	sfx := time.Now().UnixNano()

	var compID, taskID, task2ID, hostID, host2ID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING id`,
		fmt.Sprintf("ha_comp_%d", sfx)).Scan(&compID); err != nil {
		t.Fatalf("建组件失败: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO hosts (ip, hostname, arch, os_family) VALUES ($1,'ha-host-1','amd64','linux') RETURNING id`,
		fmt.Sprintf("ha-ip-1-%d", sfx)).Scan(&hostID); err != nil {
		t.Fatalf("建主机 1 失败: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO hosts (ip, hostname, arch, os_family, agent_id) VALUES ($1,'ha-host-2','amd64','linux','ha-agent') RETURNING id`,
		fmt.Sprintf("ha-ip-2-%d", sfx)).Scan(&host2ID); err != nil {
		t.Fatalf("建主机 2 失败: %v", err)
	}
	defer func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id IN ($1,$2)`, taskID, task2ID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
		db.Exec(`DELETE FROM hosts WHERE id IN ($1,$2)`, hostID, host2ID)
	}()

	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status) VALUES ($1,$2,'1.0.0','running') RETURNING id`,
		fmt.Sprintf("ha_task_%d", sfx), compID).Scan(&taskID); err != nil {
		t.Fatalf("建任务 1 失败: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO task_batches (task_id, batch_index, status) VALUES ($1,0,'running')`, taskID); err != nil {
		t.Fatalf("建批次失败: %v", err)
	}
	var thID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO task_hosts (task_id, host_id, batch_index, status) VALUES ($1,$2,0,'success') RETURNING id`,
		taskID, hostID).Scan(&thID); err != nil {
		t.Fatalf("建 task_host 失败: %v", err)
	}

	o := NewOrchestrator(NewService(db, nil, nil, nil))

	// 失败批次 + 自动回滚 + 暂停任务：三者应同时落库。
	if err := o.completeBatch(ctx, taskID, 0, "failed", true, true); err != nil {
		t.Fatalf("completeBatch(failed) 失败: %v", err)
	}
	var batchStatus, taskStatus, hostStatus string
	db.QueryRowContext(ctx, `SELECT status FROM task_batches WHERE task_id=$1 AND batch_index=0`, taskID).Scan(&batchStatus)
	db.QueryRowContext(ctx, `SELECT status FROM upgrade_tasks WHERE id=$1`, taskID).Scan(&taskStatus)
	db.QueryRowContext(ctx, `SELECT status FROM task_hosts WHERE id=$1`, thID).Scan(&hostStatus)
	if batchStatus != "failed" || taskStatus != "paused" || hostStatus != "rolled_back" {
		t.Fatalf("失败批次状态不一致: batch=%s task=%s host=%s", batchStatus, taskStatus, hostStatus)
	}

	// Promote 应跳过 failed 批次并恢复 running；失败主机记录保留。
	if err := NewService(db, nil, nil, nil).Promote(ctx, scope.New(1, false), taskID); err != nil {
		t.Fatalf("Promote 失败: %v", err)
	}
	db.QueryRowContext(ctx, `SELECT status FROM task_batches WHERE task_id=$1 AND batch_index=0`, taskID).Scan(&batchStatus)
	db.QueryRowContext(ctx, `SELECT status FROM upgrade_tasks WHERE id=$1`, taskID).Scan(&taskStatus)
	db.QueryRowContext(ctx, `SELECT status FROM task_hosts WHERE id=$1`, thID).Scan(&hostStatus)
	if batchStatus != "skipped" || taskStatus != "running" || hostStatus != "rolled_back" {
		t.Fatalf("Promote 后状态不一致: batch=%s task=%s host=%s", batchStatus, taskStatus, hostStatus)
	}

	// 成功批次 + 人工推进：批次 success、任务 paused、主机保持 success。
	db.ExecContext(ctx, `UPDATE upgrade_tasks SET status='running' WHERE id=$1`, taskID)
	db.ExecContext(ctx, `UPDATE task_batches SET status='running', finished_at=NULL WHERE task_id=$1 AND batch_index=0`, taskID)
	db.ExecContext(ctx, `UPDATE task_hosts SET status='success' WHERE id=$1`, thID)
	if err := o.completeBatch(ctx, taskID, 0, "success", false, true); err != nil {
		t.Fatalf("completeBatch(success) 失败: %v", err)
	}
	db.QueryRowContext(ctx, `SELECT status FROM task_batches WHERE task_id=$1 AND batch_index=0`, taskID).Scan(&batchStatus)
	db.QueryRowContext(ctx, `SELECT status FROM upgrade_tasks WHERE id=$1`, taskID).Scan(&taskStatus)
	db.QueryRowContext(ctx, `SELECT status FROM task_hosts WHERE id=$1`, thID).Scan(&hostStatus)
	if batchStatus != "success" || taskStatus != "paused" || hostStatus != "success" {
		t.Fatalf("成功批次状态不一致: batch=%s task=%s host=%s", batchStatus, taskStatus, hostStatus)
	}

	// dispatchHost 重复调用应只写一条指令。
	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status) VALUES ($1,$2,'1.0.0','running') RETURNING id`,
		fmt.Sprintf("ha_task2_%d", sfx), compID).Scan(&task2ID); err != nil {
		t.Fatalf("建任务 2 失败: %v", err)
	}
	var th2ID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO task_hosts (task_id, host_id, batch_index, status) VALUES ($1,$2,0,'pending') RETURNING id`,
		task2ID, host2ID).Scan(&th2ID); err != nil {
		t.Fatalf("建 task_host 2 失败: %v", err)
	}
	ok, err := o.dispatchHost(ctx, task2ID, th2ID, host2ID, "ha-agent", []byte(`{"v":1}`))
	if err != nil || !ok {
		t.Fatalf("首次 dispatchHost 应成功: ok=%v err=%v", ok, err)
	}
	ok, err = o.dispatchHost(ctx, task2ID, th2ID, host2ID, "ha-agent", []byte(`{"v":1}`))
	if err != nil {
		t.Fatalf("第二次 dispatchHost 出错: %v", err)
	}
	if ok {
		t.Fatal("第二次 dispatchHost 不应重复写入")
	}
	var cmdCount int
	db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commands WHERE task_host_id=$1`, th2ID).Scan(&cmdCount)
	if cmdCount != 1 {
		t.Fatalf("同一 task_host 应只有 1 条指令，实际 %d", cmdCount)
	}
	var th2Status string
	db.QueryRowContext(ctx, `SELECT status FROM task_hosts WHERE id=$1`, th2ID).Scan(&th2Status)
	if th2Status != "running" {
		t.Fatalf("task_host 状态应为 running，实际 %s", th2Status)
	}
}
