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

// TestDequeueStripsServiceNameForLegacyUninstall 卸载指令的 service_name 下发规则：
//   - 未上报 uninstall_detect 的旧 Agent：卸载指令剥离 service_name（行为等同旧版卸载示例留空）；
//   - 新 Agent：保持原样，由 Agent 自行识别卸载并跳过启服；
//   - 非卸载指令（install/update）：任何 Agent 都保持原样。
//
// 剥离发生在签名之前，签名必须按剥离后的载荷计算，否则 Agent 验签失败。
func TestDequeueStripsServiceNameForLegacyUninstall(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	ctx := context.Background()
	g := New(db)
	secret := "uninstall-strip-secret"
	sfx := time.Now().UnixNano()

	// agent_id 为 VARCHAR(64)，用短标签区分，不要把能力串拼进 ID。
	newAgent := func(label, caps string) string {
		id := fmt.Sprintf("ustrip-%s-%d", label, sfx)
		t.Cleanup(func() {
			db.Exec(`DELETE FROM commands WHERE agent_id=$1`, id)
			db.Exec(`DELETE FROM agents WHERE agent_id=$1`, id)
		})
		if _, err := db.ExecContext(ctx, `
			INSERT INTO agents (agent_id, ip, token_hash, secret, signature_modes, capabilities, status)
			VALUES ($1, '127.0.0.1', 'hash', $2, 'id,signed_path,v2', $3, 'online')`,
			id, secret, caps); err != nil {
			t.Fatalf("插入 Agent 失败: %v", err)
		}
		return id
	}
	dequeue := func(agentID string, args []string) *protocol.Command {
		t.Helper()
		payload, _ := json.Marshal(protocol.CommandPayload{
			ToVersion: "v1",
			Component: protocol.ComponentSpec{
				Name: "windows_exporter_script_uninstall", DeployMode: protocol.DeployScript,
				ServiceName: "windows_exporter", ExecArgs: args,
			},
		})
		if _, err := db.ExecContext(ctx, `
			INSERT INTO commands (agent_id, type, payload, status) VALUES ($1, 'upgrade_component', $2, 'pending')`,
			agentID, payload); err != nil {
			t.Fatalf("插入指令失败: %v", err)
		}
		cmd, err := g.Dequeue(ctx, agentID)
		if err != nil || cmd == nil {
			t.Fatalf("Dequeue 失败: cmd=%v err=%v", cmd, err)
		}
		// 按 Agent 收到的载荷重算签名：剥离后的字节必须能通过验签。
		body, _ := json.Marshal(cmd.Payload)
		env := signer.Envelope{
			Method: "CMD", Body: body,
			Timestamp: cmd.Timestamp, Nonce: cmd.Nonce, Path: cmd.SignedPath(),
		}
		if err := signer.Verify(secret, cmd.SignatureV2, env, 10*time.Minute); err != nil {
			t.Fatalf("按下发载荷校验 signature_v2 失败: %v", err)
		}
		return cmd
	}

	legacy := newAgent("old", "run_command,optional_command")
	modern := newAgent("new", "run_command,optional_command,uninstall_detect")

	cases := []struct {
		name    string
		agentID string
		args    []string
		want    string
	}{
		{"旧 Agent 卸载剥离", legacy, []string{"uninstall", "-Yes"}, ""},
		{"旧 Agent 卸载（flag 在前）剥离", legacy, []string{"-y", "remove"}, ""},
		{"旧 Agent 安装保留", legacy, []string{"install"}, "windows_exporter"},
		{"旧 Agent 更新保留", legacy, []string{"update"}, "windows_exporter"},
		{"新 Agent 卸载保留", modern, []string{"uninstall", "-Yes"}, "windows_exporter"},
		{"新 Agent 安装保留", modern, []string{"install"}, "windows_exporter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := dequeue(c.agentID, c.args)
			if got := cmd.Payload.Component.ServiceName; got != c.want {
				t.Fatalf("service_name = %q, want %q（args=%v）", got, c.want, c.args)
			}
		})
	}
}
