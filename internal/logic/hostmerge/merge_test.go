package hostmerge

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
)

// TestMergeCandidates 验证字段合并、Agent 迁移与重复行删除。
func TestMergeCandidates(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过主机合并集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	sfx := time.Now().UnixNano()
	ipA := fmt.Sprintf("198.31.%d.%d", (sfx/250)%250, sfx%250)
	ipB := fmt.Sprintf("198.32.%d.%d", (sfx/250)%250, (sfx+3)%250)
	tunnel := fmt.Sprintf("198.33.%d.%d", (sfx/250)%250, (sfx+5)%250)
	exit := fmt.Sprintf("198.34.%d.%d", (sfx/250)%250, (sfx+7)%250)
	agentID := fmt.Sprintf("merge-agent-%d", sfx)

	var idA, idB int64
	if err := db.QueryRow(`INSERT INTO hosts
		(ip, hostname, app_code, tunnel_ip, custom_fields, tenant_id, source)
		VALUES ($1,'merge-a','APP-A',$2,'{"a":"1"}',1,'consul_mgr') RETURNING id`,
		ipA, tunnel).Scan(&idA); err != nil {
		t.Fatalf("插入目标主机失败: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO hosts
		(ip, hostname, biz_group, exit_ip, agent_id, agent_status, custom_fields, tenant_id, source)
		VALUES ($1,'merge-a','GROUP-B',$2,$3,'online','{"b":"2"}',1,'agent') RETURNING id`,
		ipB, exit, agentID).Scan(&idB); err != nil {
		t.Fatalf("插入重复主机失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (agent_id, host_id, ip, token_hash, secret, status)
		VALUES ($1,$2,$3,'hash','secret','online')`, agentID, idB, ipB); err != nil {
		t.Fatalf("插入 Agent 失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID)
		db.Exec(`DELETE FROM hosts WHERE id IN ($1,$2)`, idA, idB)
	})

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergeCandidates(ctx, tx, idA, []int64{idA, idB})
	if err != nil {
		tx.Rollback()
		t.Fatalf("合并失败: %v", err)
	}
	if merged != 1 {
		tx.Rollback()
		t.Fatalf("应合并 1 台，实际 %d", merged)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var exists int
	db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE id=$1`, idB).Scan(&exists)
	if exists != 0 {
		t.Fatal("重复主机应被删除")
	}
	var appCode, bizGroup, gotExit, gotTunnel, gotAgent, custom string
	if err := db.QueryRow(`SELECT app_code, biz_group, exit_ip, tunnel_ip,
		COALESCE(agent_id,''), custom_fields::text FROM hosts WHERE id=$1`, idA).
		Scan(&appCode, &bizGroup, &gotExit, &gotTunnel, &gotAgent, &custom); err != nil {
		t.Fatal(err)
	}
	if appCode != "APP-A" || bizGroup != "GROUP-B" || gotExit != exit ||
		gotTunnel != tunnel || gotAgent != agentID {
		t.Fatalf("字段合并不符: app=%s group=%s exit=%s tunnel=%s agent=%s",
			appCode, bizGroup, gotExit, gotTunnel, gotAgent)
	}
	if custom == "" || custom == "{}" {
		t.Fatalf("自定义字段未合并: %s", custom)
	}
	var agentHost int64
	if err := db.QueryRow(`SELECT host_id FROM agents WHERE agent_id=$1`, agentID).Scan(&agentHost); err != nil {
		t.Fatal(err)
	}
	if agentHost != idA {
		t.Fatalf("Agent 应迁移到目标主机 %d，实际 %d", idA, agentHost)
	}
}
