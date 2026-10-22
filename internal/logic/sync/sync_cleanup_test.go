package sync

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

// TestCleanupMissingHosts 验证自动清理只删除「纯 Consul 且已不在本轮清单」的主机。
func TestCleanupMissingHosts(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Consul 清理集成测试")
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
	syncer := NewSyncer(db)
	prefix := fmt.Sprintf("cleanup-test-%d", time.Now().UnixNano())
	now := time.Now()
	old := now.Add(-time.Hour)

	insertHost := func(tag, source, agentID string, synced *time.Time) int64 {
		t.Helper()
		var lastSynced any
		if synced != nil {
			lastSynced = *synced
		}
		var id int64
		if err := db.QueryRow(`
			INSERT INTO hosts (ip, hostname, tenant_id, source, agent_id, last_synced_at)
			VALUES ($1, $2, 1, $3, $4, $5) RETURNING id`,
			prefix+"-"+tag, prefix, source, agentID, lastSynced).Scan(&id); err != nil {
			t.Fatalf("插入主机 %s 失败: %v", tag, err)
		}
		return id
	}

	staleID := insertHost("stale", "consul", "", &old)
	nullSyncID := insertHost("null-sync", "consul", "", nil)
	freshID := insertHost("fresh", "consul", "", &now)
	agentIDHostID := insertHost("agent-id", "consul", "agent-"+prefix, &old)
	agentRowHostID := insertHost("agent-row", "consul", "", &old)
	manualID := insertHost("manual", "manual", "", &old)
	taskHostID := insertHost("task", "consul", "", &old)
	commandHostID := insertHost("command", "consul", "", &old)

	// 有 agents 记录（即使 hosts.agent_id 为空）也应保留。
	if _, err := db.Exec(`
		INSERT INTO agents (agent_id, ip, token_hash, secret)
		VALUES ($1, $2, 'hash', 'secret')`,
		"agent-"+prefix+"-row", prefix+"-agent-row"); err != nil {
		t.Fatalf("插入 Agent 失败: %v", err)
	}

	// 有未完成任务的主机应保留。
	var compID, taskID int64
	if err := db.QueryRow(`
		INSERT INTO components (tenant_id, name, deploy_mode)
		VALUES (1, $1, 'replace') RETURNING id`, prefix+"-component").Scan(&compID); err != nil {
		t.Fatalf("插入组件失败: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO upgrade_tasks (tenant_id, name, component_id, version, status)
		VALUES (1, $1, $2, '1.0.0', 'running') RETURNING id`,
		prefix+"-task", compID).Scan(&taskID); err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO task_hosts (task_id, host_id, status) VALUES ($1, $2, 'pending')`,
		taskID, taskHostID); err != nil {
		t.Fatalf("插入 task_host 失败: %v", err)
	}

	// 有未完成指令的主机应保留。
	if _, err := db.Exec(`
		INSERT INTO commands (host_id, agent_id, type, status)
		VALUES ($1, 'agent-x', 'upgrade_component', 'pending')`, commandHostID); err != nil {
		t.Fatalf("插入指令失败: %v", err)
	}

	t.Cleanup(func() {
		db.Exec(`DELETE FROM hosts WHERE ip LIKE $1`, prefix+"%")
		db.Exec(`DELETE FROM agents WHERE ip LIKE $1`, prefix+"%")
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
	})

	deleted, err := syncer.cleanupMissingHosts(ctx, now)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("期望清理 2 台（stale + last_synced_at 为空），实际 %d", deleted)
	}

	check := func(id int64, want int, name string) {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(1) FROM hosts WHERE id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("%s: 期望存在 %d，实际 %d", name, want, n)
		}
	}
	check(staleID, 0, "过期 Consul 主机")
	check(nullSyncID, 0, "last_synced_at 为空的 Consul 主机")
	check(freshID, 1, "本轮同步到的主机")
	check(agentIDHostID, 1, "带 agent_id 的主机")
	check(agentRowHostID, 1, "有 agents 记录的主机")
	check(manualID, 1, "手工主机")
	check(taskHostID, 1, "有未完成任务的主机")
	check(commandHostID, 1, "有未完成指令的主机")
}
