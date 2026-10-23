package agentgw

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// TestReportTargetsCorrectCommand 验证 Report 按 cmdID 精确定位：
//  1. 同一 Agent 有多条在途指令时，不会把结果写到错误的指令上（串号）；
//  2. 跨 Agent 上报被拒绝（越权）。
func TestReportTargetsCorrectCommand(t *testing.T) {
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

	g := New(db)
	ctx := context.Background()

	const owner = "rep-owner-agent"
	const attacker = "rep-attacker-agent"

	db.Exec(`DELETE FROM commands WHERE agent_id IN ($1,$2)`, owner, attacker)
	defer db.Exec(`DELETE FROM commands WHERE agent_id IN ($1,$2)`, owner, attacker)

	// 同一 Agent 两条在途指令：cmd1 先下发，cmd2 后下发
	var cmd1, cmd2 int64
	if err := db.QueryRow(`INSERT INTO commands (agent_id, type, payload, status)
		VALUES ($1,'upgrade_component','{}','dispatched') RETURNING id`, owner).Scan(&cmd1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO commands (agent_id, type, payload, status)
		VALUES ($1,'upgrade_component','{}','dispatched') RETURNING id`, owner).Scan(&cmd2); err != nil {
		t.Fatal(err)
	}

	// 上报 cmd1 成功。旧实现取 "ORDER BY id DESC LIMIT 1" 会误写到 cmd2。
	err = g.Report(ctx, owner, itoa(cmd1), protocol.ReportRequest{
		Status: protocol.CmdSuccess, Stage: "done", ToVersion: "9.9.9",
	})
	if err != nil {
		t.Fatalf("本人上报应成功: %v", err)
	}

	var s1, s2 string
	db.QueryRow(`SELECT status FROM commands WHERE id=$1`, cmd1).Scan(&s1)
	db.QueryRow(`SELECT status FROM commands WHERE id=$1`, cmd2).Scan(&s2)
	if s1 != protocol.CmdSuccess {
		t.Fatalf("cmd1 应为 success，实际 %s", s1)
	}
	if s2 != "dispatched" {
		t.Fatalf("cmd2 不应被串改，实际 %s（说明存在串号）", s2)
	}
	t.Logf("✅ 指令未串号: cmd1=%s cmd2=%s", s1, s2)

	// 跨 Agent 上报 cmd2 必须被拒
	if err := g.Report(ctx, attacker, itoa(cmd2), protocol.ReportRequest{
		Status: protocol.CmdFailed, Stage: "done",
	}); err == nil {
		t.Fatal("❌ 跨 Agent 上报未被拒绝")
	} else {
		t.Logf("✅ 跨 Agent 上报被拒: %v", err)
	}

	db.QueryRow(`SELECT status FROM commands WHERE id=$1`, cmd2).Scan(&s2)
	if s2 != "dispatched" {
		t.Fatalf("受害者指令被篡改为 %s", s2)
	}
	t.Logf("✅ 受害者指令状态保持 %s", s2)
}

func itoa(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = digits[v%10]
		v /= 10
	}
	return string(b[i:])
}
