package agentgw

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

// TestAckOwnership 验证 Ack 只能确认自己的指令，不能篡改他人指令。
//
// 背景：cmdID 为 BIGSERIAL（连续可猜），若不校验归属，任何已认证 Agent
// 都能枚举 ID 篡改其它主机的升级状态（谎报成功/失败）。
func TestAckOwnership(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Skipf("数据库不可达: %v", err)
	}

	ctx := context.Background()
	g := New(db)

	// 构造两个 agent 的指令：victim 属于 agent-A，attacker 是 agent-B
	const victimAgent = "test-ack-victim-agent"
	const attackerAgent = "test-ack-attacker-agent"
	t.Cleanup(func() {
		db.Exec(`DELETE FROM commands WHERE agent_id IN ($1,$2)`, victimAgent, attackerAgent)
	})
	db.Exec(`DELETE FROM commands WHERE agent_id IN ($1,$2)`, victimAgent, attackerAgent)

	var victimCmdID int64
	err = db.QueryRowContext(ctx, `
		INSERT INTO commands (agent_id, type, payload, status)
		VALUES ($1, 'upgrade_component', '{}', 'dispatched') RETURNING id`,
		victimAgent).Scan(&victimCmdID)
	if err != nil {
		t.Fatalf("构造受害者指令失败: %v", err)
	}

	// 攻击者尝试 ack 受害者的指令
	err = g.Ack(ctx, attackerAgent, fmtInt(victimCmdID), "accepted", "")
	if err == nil {
		t.Fatal("❌ 越权：攻击者成功 ack 了他人指令")
	}
	t.Logf("✅ 跨 Agent ack 被拒绝: %v", err)

	// 受害者指令状态必须未被改动
	var status string
	db.QueryRowContext(ctx, `SELECT status FROM commands WHERE id=$1`, victimCmdID).Scan(&status)
	if status != "dispatched" {
		t.Fatalf("❌ 指令状态被越权篡改为 %s", status)
	}
	t.Logf("✅ 受害者指令状态保持 %s 未被篡改", status)

	// 本人 ack 应成功
	if err := g.Ack(ctx, victimAgent, fmtInt(victimCmdID), "accepted", ""); err != nil {
		t.Fatalf("❌ 本人 ack 应成功却失败: %v", err)
	}
	db.QueryRowContext(ctx, `SELECT status FROM commands WHERE id=$1`, victimCmdID).Scan(&status)
	if status != "accepted" {
		t.Fatalf("❌ 本人 ack 后状态应为 accepted，实际 %s", status)
	}
	t.Logf("✅ 本人 ack 正常生效: %s", status)
}

func fmtInt(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{digits[v%10]}, b...)
		v /= 10
	}
	return string(b)
}
