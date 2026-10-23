package agentgw

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

type fakeConsul struct {
	calls []string
}

func (f *fakeConsul) WriteServiceMeta(serviceID, key, value string) error {
	f.calls = append(f.calls, serviceID+"|"+key+"|"+value)
	return nil
}

// TestReportDuplicateFinalConsulAndStaleGuard 覆盖“重新执行”后的迟到上报：
//   - UpdateStage 已自动落 success 后，最终 report 仍应补写 Consul 版本；
//   - 主机已被重试（started_at 基线更新）时，旧成功指令的重复上报不能再写
//     task_host，也不能写 Consul；
//   - 同一 task_host 已有更新的活动指令时，旧终态上报同样忽略。
func TestReportDuplicateFinalConsulAndStaleGuard(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sfx := time.Now().UnixNano()
	agentID := fmt.Sprintf("dup-report-agent-%d", sfx)

	var compID, hostID, taskID, thID, cmdID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING id`,
		fmt.Sprintf("dup_comp_%d", sfx)).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO hosts (ip, hostname, agent_id, agent_status) VALUES ($1,'dup-host',$2,'online') RETURNING id`,
		fmt.Sprintf("dup-ip-%d", sfx), agentID).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status)
		 VALUES ($1,$2,'2.0.0','running') RETURNING id`,
		fmt.Sprintf("dup_task_%d", sfx), compID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO task_hosts (task_id, host_id, batch_index, status, started_at)
		 VALUES ($1,$2,0,'success',NOW()-INTERVAL '1 hour') RETURNING id`,
		taskID, hostID).Scan(&thID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, payload, status,
		                       finished_at, result, log)
		 VALUES ($1,$2,$3,$4,'upgrade_component','{"to_version":"2.0.0"}','success',
		         NOW()-INTERVAL '2 minutes','{"to_version":"2.0.0"}','') RETURNING id`,
		taskID, thID, hostID, agentID).Scan(&cmdID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	g := New(db)
	consul := &fakeConsul{}
	g.SetConsul(consul)

	// 1. 自动终态后的最终重复上报：应补写 Consul 版本。
	if err := g.Report(ctx, agentID, fmt.Sprintf("%d", cmdID), protocol.ReportRequest{
		Status: protocol.CmdSuccess, Stage: protocol.StageDone, ToVersion: "2.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	if len(consul.calls) != 1 {
		t.Fatalf("重复成功上报应补写 Consul，实际调用 %d 次", len(consul.calls))
	}

	// 2. 模拟重新执行：重置主机并更新重试基线。
	if _, err := db.ExecContext(ctx,
		`UPDATE task_hosts SET status='pending', started_at=NOW(), finished_at=NULL WHERE id=$1`, thID); err != nil {
		t.Fatal(err)
	}
	consul.calls = nil
	if err := g.Report(ctx, agentID, fmt.Sprintf("%d", cmdID), protocol.ReportRequest{
		Status: protocol.CmdSuccess, Stage: protocol.StageDone, ToVersion: "2.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	if len(consul.calls) != 0 {
		t.Fatalf("旧指令早于重试基线，不应写 Consul，实际 %v", consul.calls)
	}
	var status string
	db.QueryRowContext(ctx, `SELECT status FROM task_hosts WHERE id=$1`, thID).Scan(&status)
	if status != "pending" {
		t.Fatalf("旧上报不应改回终态，实际 %s", status)
	}

	// 3. 同一主机存在更新活动指令时，旧终态上报也必须忽略。
	if _, err := db.ExecContext(ctx,
		`UPDATE task_hosts SET started_at=NOW()-INTERVAL '1 hour' WHERE id=$1`, thID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, payload, status)
		 VALUES ($1,$2,$3,$4,'upgrade_component','{}','running')`,
		taskID, thID, hostID, agentID); err != nil {
		t.Fatal(err)
	}
	if err := g.Report(ctx, agentID, fmt.Sprintf("%d", cmdID), protocol.ReportRequest{
		Status: protocol.CmdSuccess, Stage: protocol.StageDone, ToVersion: "2.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	if len(consul.calls) != 0 {
		t.Fatalf("存在新活动指令时不应写 Consul，实际 %v", consul.calls)
	}
}
