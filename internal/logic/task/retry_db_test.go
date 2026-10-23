package task

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// TestRetryFailedAndStuckHosts 验证「重新执行」只重置失败/卡住主机：
//   - failed/rolled_back 主机和租约过期的 running 主机会被重置为 pending；
//   - 已成功的主机默认跳过；
//   - 旧活动指令被取消，避免撞唯一索引；
//   - 批次和任务状态回到可推进状态。
func TestRetryFailedAndStuckHosts(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过数据库集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("数据库迁移失败: %v", err)
	}
	ctx := context.Background()
	sfx := time.Now().UnixNano()

	var compID, taskID int64
	var hostIDs [4]int64
	var thIDs [4]int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING id`,
		fmt.Sprintf("retry_comp_%d", sfx)).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := db.QueryRowContext(ctx,
			`INSERT INTO hosts (ip, hostname, os_family, arch) VALUES ($1,$2,'linux','amd64') RETURNING id`,
			fmt.Sprintf("retry-ip-%d-%d", sfx, i), fmt.Sprintf("retry-host-%d", i)).Scan(&hostIDs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status)
		 VALUES ($1,$2,'2.0.0','failed') RETURNING id`,
		fmt.Sprintf("retry_task_%d", sfx), compID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO task_batches (task_id, batch_index, status, total) VALUES ($1,0,'failed',4)`, taskID); err != nil {
		t.Fatal(err)
	}
	statuses := []string{"failed", "running", "success", "rolled_back"}
	for i := 0; i < 4; i++ {
		if err := db.QueryRowContext(ctx,
			`INSERT INTO task_hosts (task_id, host_id, batch_index, status)
			 VALUES ($1,$2,0,$3) RETURNING id`, taskID, hostIDs[i], statuses[i]).Scan(&thIDs[i]); err != nil {
			t.Fatal(err)
		}
	}
	// failed 主机残留活动指令（历史卡单），running 主机的租约已过期。
	var cmdFailed, cmdStuck, cmdSuccess int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, payload, status,
		                       lease_expire_at, log)
		 VALUES ($1,$2,$3,'retry-agent-1','upgrade_component','{}','running',NOW()-INTERVAL '1 hour','')
		 RETURNING id`, taskID, thIDs[0], hostIDs[0]).Scan(&cmdFailed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, payload, status,
		                       lease_expire_at, log)
		 VALUES ($1,$2,$3,'retry-agent-2','upgrade_component','{}','running',NOW()-INTERVAL '1 hour','')
		 RETURNING id`, taskID, thIDs[1], hostIDs[1]).Scan(&cmdStuck); err != nil {
		t.Fatal(err)
	}
	// 成功主机已有一条终态成功指令，用于验证强制重试后的迟到重复上报。
	if err := db.QueryRowContext(ctx,
		`INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, payload, status,
		                       finished_at, log)
		 VALUES ($1,$2,$3,'retry-agent-3','upgrade_component','{}','success',NOW()-INTERVAL '1 hour','')
		 RETURNING id`, taskID, thIDs[2], hostIDs[2]).Scan(&cmdSuccess); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
		db.Exec(`DELETE FROM hosts WHERE id = ANY($1)`, hostIDs[:])
	})

	svc := NewService(db, nil, nil, nil)
	res, err := svc.Retry(ctx, scope.New(1, true), taskID, RetryRequest{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reset != 3 {
		t.Fatalf("应重置 3 台，实际 %d（跳过 %d）", res.Reset, res.SkippedCount)
	}

	check := func(i int, want string) {
		var got string
		db.QueryRowContext(ctx, `SELECT status FROM task_hosts WHERE id=$1`, thIDs[i]).Scan(&got)
		if got != want {
			t.Fatalf("task_host[%d] 状态=%s, want %s", i, got, want)
		}
	}
	check(0, "pending")
	check(1, "pending")
	check(2, "success") // 成功主机不被重跑
	check(3, "pending") // 已回滚按失败处理

	var cmdStatus string
	db.QueryRowContext(ctx, `SELECT status FROM commands WHERE id=$1`, cmdFailed).Scan(&cmdStatus)
	if cmdStatus != "canceled" {
		t.Fatalf("旧活动指令应取消，实际 %s", cmdStatus)
	}
	db.QueryRowContext(ctx, `SELECT status FROM commands WHERE id=$1`, cmdStuck).Scan(&cmdStatus)
	if cmdStatus != "canceled" {
		t.Fatalf("卡单指令应取消，实际 %s", cmdStatus)
	}
	var batchStatus, taskStatus string
	db.QueryRowContext(ctx, `SELECT status FROM task_batches WHERE task_id=$1`, taskID).Scan(&batchStatus)
	db.QueryRowContext(ctx, `SELECT status FROM upgrade_tasks WHERE id=$1`, taskID).Scan(&taskStatus)
	if batchStatus != "pending" || taskStatus != "running" {
		t.Fatalf("批次/任务未恢复: batch=%s task=%s", batchStatus, taskStatus)
	}

	// 对账不能把重试基线之前的旧 failed/timeout 重新写回 pending 主机。
	if _, err := agentgw.New(db).ReconcileTasks(ctx); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	check(0, "pending")
	check(1, "pending")
	check(3, "pending")

	// 被取消的旧指令迟到上报必须忽略，不能把刚重置的 pending 改成 failed。
	if err := agentgw.New(db).Report(ctx, "retry-agent-1", fmt.Sprintf("%d", cmdFailed),
		protocol.ReportRequest{Status: protocol.CmdFailed, Stage: protocol.StageFailed}); err != nil {
		t.Fatalf("迟到上报处理失败: %v", err)
	}
	check(0, "pending")

	// 再次任务级重试：pending 主机不应被重复重置。
	res2, err := svc.Retry(ctx, scope.New(1, true), taskID, RetryRequest{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Reset != 0 {
		t.Fatalf("任务级重试不应重复处理 pending 主机，实际重置 %d", res2.Reset)
	}

	// 显式强制重试成功主机：只有单台强制才允许重跑。
	res3, err := svc.Retry(ctx, scope.New(1, true), taskID, RetryRequest{
		TaskHostIDs: []int64{thIDs[2]}, Force: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res3.Reset != 1 {
		t.Fatalf("单台强制重试应重置 1 台，实际 %d", res3.Reset)
	}
	check(2, "pending")

	// 旧成功指令的迟到重复上报不能把强制重试后的 pending 改回 success。
	if err := agentgw.New(db).Report(ctx, "retry-agent-3", fmt.Sprintf("%d", cmdSuccess),
		protocol.ReportRequest{Status: protocol.CmdSuccess, ToVersion: "2.0.0"}); err != nil {
		t.Fatalf("重复成功上报处理失败: %v", err)
	}
	check(2, "pending")
}
