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
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// TestReportSanitizesLogAndFinishesTaskHost 覆盖线上卡单的根因：
// 旧 Agent 的最终日志可能包含 NUL/非法 UTF-8，版本命令输出可能超过 VARCHAR(64)。
// 旧实现会让整个 Report 事务失败，commands/task_hosts 永久停在 running。
func TestReportSanitizesLogAndFinishesTaskHost(t *testing.T) {
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
	agentID := fmt.Sprintf("report-sanitize-agent-%d", sfx)

	var compID, hostID, taskID, thID, cmdID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING id`,
		fmt.Sprintf("report_sanitize_comp_%d", sfx)).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO hosts (ip, hostname, agent_id, maintenance_until, maintenance_reason, agent_status)
		 VALUES ($1,'sanitize-host',$2,NOW()+INTERVAL '10 minutes','upgrading','upgrading') RETURNING id`,
		fmt.Sprintf("sanitize-ip-%d", sfx), agentID).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status)
		 VALUES ($1,$2,'2.0.0','running') RETURNING id`,
		fmt.Sprintf("report_sanitize_task_%d", sfx), compID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO task_hosts (task_id, host_id, status) VALUES ($1,$2,'running') RETURNING id`,
		taskID, hostID).Scan(&thID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, payload, status, last_stage, last_stage_at)
		 VALUES ($1,$2,$3,$4,'upgrade_component','{"to_version":"2.0.0"}','running','done',NOW())
		 RETURNING id`, taskID, thID, hostID, agentID).Scan(&cmdID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	g := New(db)
	badLog := "脚本输出:\x00\n" + string([]byte{0xff, 0xfe}) + "完成"
	req := protocol.ReportRequest{
		Status:    protocol.CmdSuccess,
		Stage:     protocol.StageDone,
		ToVersion: strings.Repeat("9", 200), // 超过 task_hosts.to_version VARCHAR(64)
		Log:       badLog,
	}
	if err := g.Report(ctx, agentID, fmt.Sprintf("%d", cmdID), req); err != nil {
		t.Fatalf("含 NUL/非法 UTF-8 的上报不应失败: %v", err)
	}

	var cmdStatus, thStatus, storedLog, toVersion, hostStatus string
	if err := db.QueryRowContext(ctx,
		`SELECT status, COALESCE(log,'') FROM commands WHERE id=$1`, cmdID).
		Scan(&cmdStatus, &storedLog); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT status, COALESCE(to_version,'') FROM task_hosts WHERE id=$1`, thID).
		Scan(&thStatus, &toVersion); err != nil {
		t.Fatal(err)
	}
	db.QueryRowContext(ctx, `SELECT agent_status FROM hosts WHERE id=$1`, hostID).Scan(&hostStatus)

	if cmdStatus != protocol.CmdSuccess || thStatus != "success" {
		t.Fatalf("终态未落库: command=%s task_host=%s", cmdStatus, thStatus)
	}
	if len(toVersion) > 64 {
		t.Fatalf("to_version 未截断: %d", len(toVersion))
	}
	if strings.ContainsRune(storedLog, 0) {
		t.Fatal("日志仍包含 NUL")
	}
	if hostStatus != "online" {
		t.Fatalf("成功后主机应退出 upgrading，实际 %s", hostStatus)
	}
}
