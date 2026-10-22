package agentgw

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/signer"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// TestAckSignatureFallback 验证旧 Agent 的签名路径自动探测：
//   - 数据库默认 id，但 Agent 只认 SignedPath：首次拒绝后自动切换并重排；
//   - 重排后的 legacy signature 必须能按 SignedPath 校验；
//   - 第二次仍拒绝时不得无限重排，指令应进入 failed。
func TestAckSignatureFallback(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	ctx := context.Background()
	g := New(db)
	const agentID = "test-ack-fallback-agent"
	const secret = "test-secret"
	t.Cleanup(func() {
		db.Exec(`DELETE FROM commands WHERE agent_id=$1`, agentID)
		db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID)
	})
	db.Exec(`DELETE FROM commands WHERE agent_id=$1`, agentID)
	db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID)

	if _, err := db.Exec(`INSERT INTO agents (agent_id, ip, token_hash, secret, signature_modes, status)
		VALUES ($1,'127.0.0.1','hash',$2,'id','online')`, agentID, secret); err != nil {
		t.Fatal(err)
	}
	var cmdID int64
	if err := db.QueryRow(`INSERT INTO commands (agent_id, type, payload, status, attempts)
		VALUES ($1,'self_update','{"to_version":"v2"}'::jsonb,'dispatched',1) RETURNING id`, agentID).
		Scan(&cmdID); err != nil {
		t.Fatal(err)
	}

	if err := g.Ack(ctx, agentID, fmtInt(cmdID), "rejected", "signer: 签名不匹配"); err != nil {
		t.Fatalf("首次拒绝应触发重排: %v", err)
	}
	var status, modes string
	var attempts int
	if err := db.QueryRow(`SELECT status, attempts FROM commands WHERE id=$1`, cmdID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT signature_modes FROM agents WHERE agent_id=$1`, agentID).Scan(&modes); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 || modes != protocol.SignatureModeSignedPath {
		t.Fatalf("首次拒绝后应 pending/signed_path，实际 status=%s attempts=%d modes=%s", status, attempts, modes)
	}

	cmd, err := g.Dequeue(ctx, agentID)
	if err != nil || cmd == nil {
		t.Fatalf("重排后应能取到指令: cmd=%v err=%v", cmd, err)
	}
	body, _ := json.Marshal(cmd.Payload)
	env := signer.Envelope{Method: "CMD", Path: cmd.SignedPath(), Body: body,
		Timestamp: cmd.Timestamp, Nonce: cmd.Nonce}
	if err := signer.Verify(secret, cmd.Signature, env, 10*time.Minute); err != nil {
		t.Fatalf("重排后的 legacy signature 应按 SignedPath 校验: %v", err)
	}

	// 第二次仍拒绝：attempts 已为 2，不能再切换，必须失败。
	if err := g.Ack(ctx, agentID, fmtInt(cmdID), "rejected", "signer: 签名不匹配"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM commands WHERE id=$1`, cmdID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != protocol.CmdFailed {
		t.Fatalf("第二次拒绝后应 failed，实际 %s", status)
	}
}
