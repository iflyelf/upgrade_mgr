package agentgw

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
)

// TestFinalizeCompletedStageAndSyncTaskHost 覆盖旧 Agent 的卡单：
// 阶段日志已经走到“验证版本/完成”，但最终 report 丢失；对账后应补记成功，
// 并把 task_hosts 从 running 同步为 success。
func TestFinalizeCompletedStageAndSyncTaskHost(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sfx := time.Now().UnixNano()
	agentID := fmt.Sprintf("reap-agent-%d", sfx)

	var compID, hostID, taskID, thID, cmdID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING id`,
		fmt.Sprintf("reap_comp_%d", sfx)).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO hosts (ip, hostname, agent_id, maintenance_until, maintenance_reason, agent_status)
		 VALUES ($1,'reap-host',$2,NOW()+INTERVAL '10 minutes','upgrading','upgrading') RETURNING id`,
		fmt.Sprintf("reap-ip-%d", sfx), agentID).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status)
		 VALUES ($1,$2,'2.0.0','running') RETURNING id`,
		fmt.Sprintf("reap_task_%d", sfx), compID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO task_hosts (task_id, host_id, status) VALUES ($1,$2,'running') RETURNING id`,
		taskID, hostID).Scan(&thID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, payload, status,
		                       last_stage, last_stage_at, log)
		 VALUES ($1,$2,$3,$4,'upgrade_component','{"to_version":"2.0.0"}','running',
		         'done', NOW()-INTERVAL '3 minutes',
		         '[2026-01-01 00:00:00] 阶段: 下载制品
[2026-01-01 00:00:01] 阶段: 启动服务
[2026-01-01 00:00:02] 阶段: 验证版本
[2026-01-01 00:00:03] 阶段: 完成')
		 RETURNING id`, taskID, thID, hostID, agentID).Scan(&cmdID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	g := New(db)
	n, err := g.ReconcileTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("应至少修正一条卡单指令")
	}

	var cmdStatus, thStatus, hostStatus, logText string
	db.QueryRowContext(ctx, `SELECT status, COALESCE(log,'') FROM commands WHERE id=$1`, cmdID).
		Scan(&cmdStatus, &logText)
	db.QueryRowContext(ctx, `SELECT status FROM task_hosts WHERE id=$1`, thID).Scan(&thStatus)
	db.QueryRowContext(ctx, `SELECT agent_status FROM hosts WHERE id=$1`, hostID).Scan(&hostStatus)
	if cmdStatus != "success" || thStatus != "success" {
		t.Fatalf("卡单未修复: command=%s task_host=%s", cmdStatus, thStatus)
	}
	if hostStatus != "online" {
		t.Fatalf("维护窗口未清理: %s", hostStatus)
	}
	if !strings.Contains(logText, "补记为成功") {
		t.Fatalf("缺少对账说明日志: %q", logText)
	}
}
