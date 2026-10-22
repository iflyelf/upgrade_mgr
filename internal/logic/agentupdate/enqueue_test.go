package agentupdate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// TestEnqueueDedupAndCurrent 验证按主机下发 self_update：
// 已是最新哈希时跳过；下发一次后再次下发会被在途指令去重。
func TestEnqueueDedupAndCurrent(t *testing.T) {
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

	dir := t.TempDir()
	body := []byte("agent-binary-for-enqueue-test")
	if err := os.WriteFile(filepath.Join(dir, "upgrade-agent-linux-amd64"), body, 0755); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(dir, "v-test")
	target, err := r.Resolve("amd64", "", "http://upgrade.local")
	if err != nil {
		t.Fatal(err)
	}

	agentID := fmt.Sprintf("agent-update-test-%d", time.Now().UnixNano())
	ip := fmt.Sprintf("update-test-%d", time.Now().UnixNano())
	var hostID int64
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, arch, kernel, agent_id, tenant_id, source,
		                   agent_build_hash, agent_build_version)
		VALUES ($1,'update-test','amd64','5.10.0',$2,1,'agent',$3,'v-old')
		RETURNING id`, ip, agentID, target.SHA256).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM commands WHERE agent_id=$1`, agentID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	ctx := context.Background()
	req := EnqueueRequest{HostIDs: []int64{hostID}}
	res, err := Enqueue(ctx, db, scope.System(), r, req, "http://upgrade.local")
	if err != nil {
		t.Fatal(err)
	}
	if res.Enqueued != 0 || res.SkippedCount != 1 {
		t.Fatalf("已是最新版本应跳过: %+v", res)
	}

	// 模拟旧 Agent：哈希为空，应下发。
	if _, err := db.Exec(`UPDATE hosts SET agent_build_hash='' WHERE id=$1`, hostID); err != nil {
		t.Fatal(err)
	}
	res, err = Enqueue(ctx, db, scope.System(), r, req, "http://upgrade.local")
	if err != nil {
		t.Fatal(err)
	}
	if res.Enqueued != 1 {
		t.Fatalf("旧 Agent 应下发: %+v", res)
	}

	// 已有一条 pending 指令，再次下发必须去重。
	res, err = Enqueue(ctx, db, scope.System(), r, req, "http://upgrade.local")
	if err != nil {
		t.Fatal(err)
	}
	if res.Enqueued != 0 || res.SkippedCount != 1 {
		t.Fatalf("在途指令应去重: %+v", res)
	}

	var typ, status, payload string
	if err := db.QueryRow(`SELECT type, status, payload::text FROM commands WHERE agent_id=$1`, agentID).
		Scan(&typ, &status, &payload); err != nil {
		t.Fatal(err)
	}
	if typ != "self_update" || status != "pending" {
		t.Fatalf("指令类型/状态不符: %s/%s", typ, status)
	}
	if !strings.Contains(payload, target.SHA256) || !strings.Contains(payload, "/api/agent/binary/linux-amd64") {
		t.Fatalf("payload 缺少二进制信息: %s", payload)
	}
}
