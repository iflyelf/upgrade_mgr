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

// TestCancelMarksBatchesCommandsAndHosts 验证取消任务时：
//   - 未完成批次置为 canceled；
//   - 未完成指令置为 canceled；
//   - 未完成逐机记录置为 canceled；
//   - 任务本身置为 canceled。
//
// 任务详情页依赖这些状态判断“执行到哪一步”，否则取消后仍会显示执行中。
func TestCancelMarksBatchesCommandsAndHosts(t *testing.T) {
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
	var compID, hostID, taskID, thID, cmdID int64
	if err := db.QueryRow(`INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING id`,
		fmt.Sprintf("cancel_comp_%d", sfx)).Scan(&compID); err != nil {
		t.Fatalf("建组件失败: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO hosts (ip) VALUES ($1) RETURNING id`,
		fmt.Sprintf("cancel-ip-%d", sfx)).Scan(&hostID); err != nil {
		t.Fatalf("建主机失败: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO upgrade_tasks (name, component_id, version, status)
		VALUES ($1,$2,'2.0.0','running') RETURNING id`,
		fmt.Sprintf("cancel_task_%d", sfx), compID).Scan(&taskID); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_batches (task_id, batch_index, status, total)
		VALUES ($1,0,'running',1)`, taskID); err != nil {
		t.Fatalf("建批次失败: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO task_hosts (task_id, host_id, status)
		VALUES ($1,$2,'running') RETURNING id`, taskID, hostID).Scan(&thID); err != nil {
		t.Fatalf("建逐机记录失败: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, status)
		VALUES ($1,$2,$3,'cancel-agent','upgrade_component','running') RETURNING id`,
		taskID, thID, hostID).Scan(&cmdID); err != nil {
		t.Fatalf("建指令失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	if err := NewService(db, nil, nil, nil).Cancel(ctx, scope.New(1, false), taskID); err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}

	var taskStatus, batchStatus, thStatus, cmdStatus string
	db.QueryRow(`SELECT status FROM upgrade_tasks WHERE id=$1`, taskID).Scan(&taskStatus)
	db.QueryRow(`SELECT status FROM task_batches WHERE task_id=$1 AND batch_index=0`, taskID).Scan(&batchStatus)
	db.QueryRow(`SELECT status FROM task_hosts WHERE id=$1`, thID).Scan(&thStatus)
	db.QueryRow(`SELECT status FROM commands WHERE id=$1`, cmdID).Scan(&cmdStatus)
	if taskStatus != "canceled" || batchStatus != "canceled" || thStatus != "canceled" || cmdStatus != "canceled" {
		t.Fatalf("取消状态不一致: task=%s batch=%s task_host=%s command=%s",
			taskStatus, batchStatus, thStatus, cmdStatus)
	}
}
