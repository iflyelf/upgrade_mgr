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
)

// TestPausedTaskFinishesWhenAllBatchesDone 复现并防止回归：
// auto_promote=false 时最后一批完成后任务是 paused，编排器此前只扫描 running，
// 导致任务永远显示「已暂停」，实际已全部完成。
func TestPausedTaskFinishesWhenAllBatchesDone(t *testing.T) {
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

	var compID, taskID, hostID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING id`,
		fmt.Sprintf("pause_comp_%d", sfx)).Scan(&compID); err != nil {
		t.Fatalf("建组件失败: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO hosts (ip, hostname, arch, os_family) VALUES ($1,'pause-host','amd64','linux') RETURNING id`,
		fmt.Sprintf("pause-ip-%d", sfx)).Scan(&hostID); err != nil {
		t.Fatalf("建主机失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status, strategy)
		 VALUES ($1,$2,'1.0.0','paused',$3) RETURNING id`,
		fmt.Sprintf("pause_task_%d", sfx), compID, `{"mode":"count","batch_size":10,"auto_promote":false}`).
		Scan(&taskID); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO task_batches (task_id, batch_index, status) VALUES ($1,0,'success')`, taskID); err != nil {
		t.Fatalf("建批次失败: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO task_hosts (task_id, host_id, batch_index, status) VALUES ($1,$2,0,'success')`,
		taskID, hostID); err != nil {
		t.Fatalf("建 task_host 失败: %v", err)
	}

	o := NewOrchestrator(NewService(db, nil, nil, nil))
	var st Strategy
	_ = st
	// 直接推进：所有批次已完成 → 应终结为 success。
	if err := o.advance(ctx, taskID, compID, "1.0.0", Strategy{}, 1, KindUpgrade, nil, "paused"); err != nil {
		t.Fatalf("advance 失败: %v", err)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM upgrade_tasks WHERE id=$1`, taskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "success" {
		t.Fatalf("全部批次完成后应终结为 success，实际 %q（任务卡在暂停）", status)
	}
}

// TestPausedTaskNotAutoDispatched paused 任务不得自动下发下一批。
func TestPausedTaskNotAutoDispatched(t *testing.T) {
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

	var compID, taskID, hostID, host2ID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING id`,
		fmt.Sprintf("pause2_comp_%d", sfx)).Scan(&compID); err != nil {
		t.Fatalf("建组件失败: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO hosts (ip, hostname, arch, os_family, agent_id) VALUES ($1,'pause2-host','amd64','linux','pause2-agent') RETURNING id`,
		fmt.Sprintf("pause2-ip-%d", sfx)).Scan(&hostID); err != nil {
		t.Fatalf("建主机失败: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO hosts (ip, hostname, arch, os_family, agent_id) VALUES ($1,'pause2-host2','amd64','linux','pause2-agent2') RETURNING id`,
		fmt.Sprintf("pause2-ip2-%d", sfx)).Scan(&host2ID); err != nil {
		t.Fatalf("建主机 2 失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM commands WHERE task_id=$1`, taskID)
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
		db.Exec(`DELETE FROM hosts WHERE id IN ($1,$2)`, hostID, host2ID)
	})

	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status, strategy)
		 VALUES ($1,$2,'1.0.0','paused',$3) RETURNING id`,
		fmt.Sprintf("pause2_task_%d", sfx), compID, `{"mode":"count","batch_size":10,"auto_promote":true}`).
		Scan(&taskID); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	// 第一批已完成，第二批 pending。
	if _, err := db.ExecContext(ctx,
		`INSERT INTO task_batches (task_id, batch_index, status) VALUES ($1,0,'success'),($1,1,'pending')`,
		taskID); err != nil {
		t.Fatalf("建批次失败: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO task_hosts (task_id, host_id, batch_index, status) VALUES ($1,$2,0,'success'),($1,$3,1,'pending')`,
		taskID, hostID, host2ID); err != nil {
		t.Fatalf("建 task_host 失败: %v", err)
	}

	o := NewOrchestrator(NewService(db, nil, nil, nil))
	if err := o.advance(ctx, taskID, compID, "1.0.0", Strategy{AutoPromote: true}, 1, KindUpgrade, nil, "paused"); err != nil {
		t.Fatalf("advance 失败: %v", err)
	}
	var pending int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM commands WHERE task_id=$1`, taskID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("暂停任务不应自动下发指令，实际生成 %d 条", pending)
	}
	var status string
	db.QueryRowContext(ctx, `SELECT status FROM upgrade_tasks WHERE id=$1`, taskID).Scan(&status)
	if status != "paused" {
		t.Fatalf("任务应保持 paused，实际 %q", status)
	}
}
