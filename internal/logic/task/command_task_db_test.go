package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// TestCreateCommandTask 验证一次性命令任务：
//   - 不要求 component_id，脚本落库到 command 列；
//   - 编排器下发 run_command 指令，载荷携带脚本；
//   - 未声明 run_command 能力的 Agent 被跳过并写入原因。
func TestCreateCommandTask(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过数据库集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("数据库迁移失败: %v", err)
	}
	ctx := context.Background()
	sfx := time.Now().UnixNano()

	newHost := func(ip, hostname, agentID string) int64 {
		var id int64
		if err := db.QueryRowContext(ctx,
			`INSERT INTO hosts (ip, hostname, arch, os_family, agent_id, last_seen) VALUES ($1,$2,'amd64','linux',$3,NOW()) RETURNING id`,
			ip, hostname, agentID).Scan(&id); err != nil {
			t.Fatalf("建主机失败: %v", err)
		}
		return id
	}
	// 用合法且本次测试唯一的 IP，IP 表达式会做真实解析。
	ipOK := fmt.Sprintf("10.%d.%d.1", (sfx/65536)%200+20, (sfx/256)%200)
	ipOld := fmt.Sprintf("10.%d.%d.2", (sfx/65536)%200+20, (sfx/256)%200)
	hostOK := newHost(ipOK, fmt.Sprintf("cmd-ok-%d", sfx), "cmd-agent-ok")
	hostOld := newHost(ipOld, fmt.Sprintf("cmd-old-%d", sfx), "cmd-agent-old")
	db.Exec(`DELETE FROM agents WHERE agent_id IN ('cmd-agent-ok','cmd-agent-old')`)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO agents (agent_id, host_id, ip, token_hash, secret, capabilities, status)
		 VALUES ('cmd-agent-ok',$1,$2,'h','s',$3,'online')`,
		hostOK, ipOK, protocol.AgentCapabilityRunCommand); err != nil {
		t.Fatalf("建 Agent 失败: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO agents (agent_id, host_id, ip, token_hash, secret, capabilities, status)
		 VALUES ('cmd-agent-old',$1,$2,'h','s','','online')`, hostOld, ipOld); err != nil {
		t.Fatalf("建旧 Agent 失败: %v", err)
	}

	var taskID int64
	defer func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM agents WHERE agent_id IN ('cmd-agent-ok','cmd-agent-old')`)
		db.Exec(`DELETE FROM hosts WHERE id IN ($1,$2)`, hostOK, hostOld)
	}()

	svc := NewService(db, component.NewRepo(db), nil, func() bool { return true })
	req := CreateRequest{
		Kind:    KindCommand,
		Name:    fmt.Sprintf("cmd-task-%d", sfx),
		Command: protocol.CommandSpec{Script: "echo hello", Stdin: "Y\n", TTY: true, Timeout: "5m"},
		Selector: Selector{
			IPExpr: fmt.Sprintf("%s,%s", ipOK, ipOld),
		},
		Strategy: Strategy{Mode: "count", BatchSize: 10, AutoPromote: true},
	}
	taskID, err = svc.Create(ctx, scope.New(1, true), req)
	if err != nil {
		t.Fatalf("创建命令任务失败: %v", err)
	}

	var kind string
	var componentID sql.NullInt64
	var raw []byte
	if err := db.QueryRowContext(ctx,
		`SELECT kind, component_id, command FROM upgrade_tasks WHERE id=$1`, taskID).
		Scan(&kind, &componentID, &raw); err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if kind != KindCommand || componentID.Valid {
		t.Fatalf("命令任务应为 kind=command 且 component_id 为空: kind=%s component=%v", kind, componentID)
	}
	var saved protocol.CommandSpec
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("命令 JSON 无效: %v", err)
	}
	if saved.Script != "echo hello" || saved.Stdin != "Y\n" || !saved.TTY {
		t.Fatalf("命令字段未正确保存: %+v", saved)
	}

	// 启动任务并由编排器下发。
	if err := svc.Start(ctx, scope.New(1, true), taskID); err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	o := NewOrchestrator(svc)
	if err := o.advance(ctx, taskID, 0, "command",
		Strategy{Mode: "count", BatchSize: 10, AutoPromote: true}, 1, KindCommand, raw, "running"); err != nil {
		t.Fatalf("推进任务失败: %v", err)
	}

	// 支持的 Agent 应收到 run_command，且载荷里有脚本。
	var cmdType string
	var payload []byte
	if err := db.QueryRowContext(ctx, `
		SELECT c.type, c.payload FROM commands c
		JOIN task_hosts th ON th.id=c.task_host_id
		WHERE th.task_id=$1 AND th.host_id=$2`, taskID, hostOK).
		Scan(&cmdType, &payload); err != nil {
		t.Fatalf("未下发指令给支持能力的 Agent: %v", err)
	}
	if cmdType != protocol.CmdRunCommand {
		t.Fatalf("指令类型应为 %s，实际 %s", protocol.CmdRunCommand, cmdType)
	}
	var pl protocol.CommandPayload
	if err := json.Unmarshal(payload, &pl); err != nil {
		t.Fatalf("指令载荷无效: %v", err)
	}
	if pl.Command == nil || pl.Command.Script != "echo hello" {
		t.Fatalf("载荷未携带脚本: %+v", pl)
	}

	// 旧 Agent 应被跳过，并记录原因。
	var status, log string
	if err := db.QueryRowContext(ctx,
		`SELECT status, COALESCE(log,'') FROM task_hosts WHERE task_id=$1 AND host_id=$2`,
		taskID, hostOld).Scan(&status, &log); err != nil {
		t.Fatalf("读取旧 Agent 主机失败: %v", err)
	}
	if status != "skipped" || !strings.Contains(log, "不支持执行命令任务") {
		t.Fatalf("旧 Agent 应被跳过并说明原因: status=%s log=%q", status, log)
	}
}

// TestCreateCommandTaskValidation 校验命令任务的入参约束。
func TestCreateCommandTaskValidation(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过数据库集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	svc := NewService(db, component.NewRepo(db), nil, func() bool { return false })
	_, err = svc.Create(context.Background(), scope.New(1, true), CreateRequest{
		Kind:     KindCommand,
		Command:  protocol.CommandSpec{Script: "echo hi"},
		Selector: Selector{},
	})
	if err == nil || !strings.Contains(err.Error(), "allow_exec_mode") {
		t.Fatalf("禁止 exec 模式时应拒绝创建命令任务: %v", err)
	}
}
