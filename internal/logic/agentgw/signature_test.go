package agentgw

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/signer"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// TestDequeueSendsLegacyAndV2Signature 验证服务端同时下发两套签名：
//   - signature   只签 ID，旧 Agent 可直接校验，服务端升级无需重装；
//   - signature_v2 签 ID|类型|过期时间，新 Agent 优先使用。
func TestDequeueSendsLegacyAndV2Signature(t *testing.T) {
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
	agentID := fmt.Sprintf("sig-test-agent-%d", time.Now().UnixNano())
	secret := "sig-test-secret"
	t.Cleanup(func() {
		db.Exec(`DELETE FROM commands WHERE agent_id=$1`, agentID)
		db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID)
	})

	if _, err := db.ExecContext(ctx, `
		INSERT INTO agents (agent_id, ip, token_hash, secret, status)
		VALUES ($1, '127.0.0.1', 'hash', $2, 'online')`, agentID, secret); err != nil {
		t.Fatalf("插入 Agent 失败: %v", err)
	}
	var cmdID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO commands (agent_id, type, payload, status)
		VALUES ($1, 'upgrade_component', '{"to_version":"1.0.0"}', 'pending')
		RETURNING id`, agentID).Scan(&cmdID); err != nil {
		t.Fatalf("插入指令失败: %v", err)
	}

	cmd, err := g.Dequeue(ctx, agentID)
	if err != nil {
		t.Fatalf("Dequeue 失败: %v", err)
	}
	if cmd == nil {
		t.Fatal("Dequeue 应返回指令")
	}
	if cmd.Signature == "" || cmd.SignatureV2 == "" {
		t.Fatalf("必须同时下发 signature 与 signature_v2: %+v", cmd)
	}

	body, _ := json.Marshal(cmd.Payload)
	env := signer.Envelope{
		Method: "CMD", Body: body,
		Timestamp: cmd.Timestamp, Nonce: cmd.Nonce,
	}

	env.Path = cmd.ID
	if err := signer.Verify(secret, cmd.Signature, env, 10*time.Minute); err != nil {
		t.Fatalf("旧 Agent 校验 signature 失败: %v", err)
	}

	env.Path = cmd.SignedPath()
	if err := signer.Verify(secret, cmd.SignatureV2, env, 10*time.Minute); err != nil {
		t.Fatalf("新 Agent 校验 signature_v2 失败: %v", err)
	}

	// 证明两套签名确实不同：旧签名不能通过新版 Path 校验。
	if err := signer.Verify(secret, cmd.Signature, env, 10*time.Minute); err == nil {
		t.Fatal("signature 不应使用 SignedPath 校验通过")
	}
}

// TestDequeueUsesAdvertisedLegacySignature 验证服务端按 Agent 上报的能力选择
// legacy signature 的 Path，而不是依赖 Agent 版本推断。
func TestDequeueUsesAdvertisedLegacySignature(t *testing.T) {
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
	cases := []struct {
		name       string
		modes      string
		wantLegacy string
		otherPath  func(cmd *protocol.Command) string
	}{
		{
			name: "only_id", modes: "id", wantLegacy: protocol.SignatureModeID,
			otherPath: func(cmd *protocol.Command) string { return cmd.SignedPath() },
		},
		{
			name: "only_signed_path", modes: "signed_path", wantLegacy: protocol.SignatureModeSignedPath,
			otherPath: func(cmd *protocol.Command) string { return cmd.ID },
		},
		{
			name: "all_prefers_signed_path", modes: "id,signed_path,v2", wantLegacy: protocol.SignatureModeSignedPath,
			otherPath: func(cmd *protocol.Command) string { return cmd.ID },
		},
		{
			name: "v2_only_falls_back_id", modes: "v2", wantLegacy: protocol.SignatureModeID,
			otherPath: func(cmd *protocol.Command) string { return cmd.SignedPath() },
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agentID := fmt.Sprintf("sig-mode-%d-%d", time.Now().UnixNano(), i)
			secret := "sig-mode-secret"
			t.Cleanup(func() {
				db.Exec(`DELETE FROM commands WHERE agent_id=$1`, agentID)
				db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID)
			})
			if _, err := db.ExecContext(ctx, `
				INSERT INTO agents (agent_id, ip, token_hash, secret, signature_modes, status)
				VALUES ($1, '127.0.0.1', 'hash', $2, $3, 'online')`,
				agentID, secret, tc.modes); err != nil {
				t.Fatalf("插入 Agent 失败: %v", err)
			}
			if _, err := db.ExecContext(ctx, `
				INSERT INTO commands (agent_id, type, payload, status)
				VALUES ($1, 'upgrade_component', '{"to_version":"1.0.0"}', 'pending')`,
				agentID); err != nil {
				t.Fatalf("插入指令失败: %v", err)
			}

			cmd, err := g.Dequeue(ctx, agentID)
			if err != nil {
				t.Fatalf("Dequeue 失败: %v", err)
			}
			if cmd == nil {
				t.Fatal("Dequeue 应返回指令")
			}
			body, _ := json.Marshal(cmd.Payload)
			env := signer.Envelope{
				Method: "CMD", Body: body,
				Timestamp: cmd.Timestamp, Nonce: cmd.Nonce,
			}
			if tc.wantLegacy == protocol.SignatureModeSignedPath {
				env.Path = cmd.SignedPath()
			} else {
				env.Path = cmd.ID
			}
			if err := signer.Verify(secret, cmd.Signature, env, 10*time.Minute); err != nil {
				t.Fatalf("legacy signature 未按能力 %s 签名: %v", tc.wantLegacy, err)
			}
			env.Path = tc.otherPath(cmd)
			if err := signer.Verify(secret, cmd.Signature, env, 10*time.Minute); err == nil {
				t.Fatalf("legacy signature 不应同时通过另一条 Path")
			}

			env.Path = cmd.SignedPath()
			if err := signer.Verify(secret, cmd.SignatureV2, env, 10*time.Minute); err != nil {
				t.Fatalf("signature_v2 校验失败: %v", err)
			}
		})
	}
}
