package agentgw

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
)

// TestUpdateStageLogsAndTaskHostStage 验证 Agent 阶段上报同时更新：
//   - commands.log（任务详情可见的执行链路）；
//   - commands.status（dispatched/accepted -> running）；
//   - task_hosts.stage（当前步骤）；
//   - hosts.last_stage。
//
// 指令进入终态后只更新主机阶段，不再追加日志，避免重复噪声。
func TestUpdateStageLogsAndTaskHostStage(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer db.Close()
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	ctx := context.Background()
	g := New(db)
	sfx := time.Now().UnixNano()
	agentID := fmt.Sprintf("stage-agent-%d", sfx)
	ip := fmt.Sprintf("stage-ip-%d", sfx)
	compName := fmt.Sprintf("stage_comp_%d", sfx)

	var compID, taskID, hostID, thID, cmdID int64
	if err := db.QueryRow(`
		INSERT INTO components (name, deploy_mode) VALUES ($1, 'replace') RETURNING id`,
		compName).Scan(&compID); err != nil {
		t.Fatalf("插入组件失败: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, agent_id) VALUES ($1, 'stage-host', $2) RETURNING id`,
		ip, agentID).Scan(&hostID); err != nil {
		t.Fatalf("插入主机失败: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO upgrade_tasks (name, component_id, version, status)
		VALUES ($1, $2, '2.0.0', 'running') RETURNING id`,
		fmt.Sprintf("stage_task_%d", sfx), compID).Scan(&taskID); err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO task_hosts (task_id, host_id, status) VALUES ($1, $2, 'running') RETURNING id`,
		taskID, hostID).Scan(&thID); err != nil {
		t.Fatalf("插入逐机记录失败: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, payload, status)
		VALUES ($1, $2, $3, $4, 'upgrade_component', '{}', 'dispatched') RETURNING id`,
		taskID, thID, hostID, agentID).Scan(&cmdID); err != nil {
		t.Fatalf("插入指令失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	cmdIDStr := strconv.FormatInt(cmdID, 10)
	if err := g.UpdateStage(ctx, agentID, cmdIDStr, "downloading"); err != nil {
		t.Fatalf("阶段上报失败: %v", err)
	}
	var cmdStatus, cmdLog, thStage, hostStage string
	if err := db.QueryRow(`SELECT status, log FROM commands WHERE id=$1`, cmdID).
		Scan(&cmdStatus, &cmdLog); err != nil {
		t.Fatal(err)
	}
	if cmdStatus != "running" || !strings.Contains(cmdLog, "下载制品") {
		t.Fatalf("阶段日志/状态未落库: status=%s log=%q", cmdStatus, cmdLog)
	}
	db.QueryRow(`SELECT stage FROM task_hosts WHERE id=$1`, thID).Scan(&thStage)
	db.QueryRow(`SELECT last_stage FROM hosts WHERE id=$1`, hostID).Scan(&hostStage)
	if thStage != "downloading" || hostStage != "downloading" {
		t.Fatalf("阶段未同步到逐机/主机: task_host=%q host=%q", thStage, hostStage)
	}

	if err := g.UpdateStage(ctx, agentID, cmdIDStr, "done"); err != nil {
		t.Fatalf("完成阶段上报失败: %v", err)
	}
	db.QueryRow(`SELECT log FROM commands WHERE id=$1`, cmdID).Scan(&cmdLog)
	if !strings.Contains(cmdLog, "下载制品") || !strings.Contains(cmdLog, "完成") {
		t.Fatalf("阶段日志应累加: %q", cmdLog)
	}

	// 终态指令不再追加日志，但主机当前阶段仍更新。
	if _, err := db.Exec(`UPDATE commands SET status='success' WHERE id=$1`, cmdID); err != nil {
		t.Fatal(err)
	}
	if err := g.UpdateStage(ctx, agentID, cmdIDStr, "backup"); err != nil {
		t.Fatalf("终态阶段上报失败: %v", err)
	}
	var logAfter string
	db.QueryRow(`SELECT log FROM commands WHERE id=$1`, cmdID).Scan(&logAfter)
	if strings.Contains(logAfter, "备份当前版本") {
		t.Fatalf("终态指令不应再追加阶段日志: %q", logAfter)
	}
	db.QueryRow(`SELECT last_stage FROM hosts WHERE id=$1`, hostID).Scan(&hostStage)
	if hostStage != "backup" {
		t.Fatalf("终态后主机阶段仍应更新，实际 %q", hostStage)
	}
}
