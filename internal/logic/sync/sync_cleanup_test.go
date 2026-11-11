package sync

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"

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
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	ctx := context.Background()
	syncer := NewSyncer(db)
	// 破坏性测试保护：生产库若已有 source=consul 主机，直接跳过，
	// 避免 cleanupMissingHosts 清理到真实数据。
	var existingConsul int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE source='consul'`).Scan(&existingConsul); err != nil {
		t.Fatalf("检查现有主机失败: %v", err)
	}
	if existingConsul > 0 {
		t.Skip("数据库中已有 source=consul 主机，跳过破坏性清理测试")
	}
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
	onlineID := insertHost("online", "consul", "", &old)
	if _, err := db.Exec(`UPDATE hosts SET agent_status='online' WHERE id=$1`, onlineID); err != nil {
		t.Fatalf("标记在线主机失败: %v", err)
	}

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
	// 清理是全库行为，测试库里可能还有其他测试遗留的过期 Consul 主机，
	// 因此只断言本用例目标主机被删除，不锁定全局删除总数。
	if deleted < 2 {
		t.Fatalf("期望至少清理 2 台，实际 %d", deleted)
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
	check(onlineID, 1, "在线主机")
}

// TestCleanupMissingMgrHosts 验证 consul_mgr 清理只影响当前服务组。
func TestCleanupMissingMgrHosts(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 consul_mgr 清理集成测试")
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
	syncer := NewSyncer(db)
	prefix := fmt.Sprintf("mgr-cleanup-%d", time.Now().UnixNano())
	// 使用随机高位 group_id，避免与生产服务组（1、2...）冲突。
	groupID := int64(900000000 + time.Now().UnixNano()%100000000)
	groupStr := fmt.Sprintf("%d", groupID)
	var existing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE source='consul_mgr' AND source_group=$1`, groupStr).Scan(&existing); err != nil {
		t.Fatalf("检查现有主机失败: %v", err)
	}
	if existing > 0 {
		t.Skip("测试服务组已有主机，跳过破坏性清理测试")
	}
	now := time.Now()
	old := now.Add(-time.Hour)
	insert := func(tag, source, group string, synced time.Time) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRow(`
			INSERT INTO hosts (ip, hostname, tenant_id, source, source_group, last_synced_at)
			VALUES ($1, $2, 1, $3, $4, $5) RETURNING id`,
			prefix+"-"+tag, tag, source, group, synced).Scan(&id); err != nil {
			t.Fatalf("插入主机 %s 失败: %v", tag, err)
		}
		return id
	}
	g1Stale := insert("g1-stale", "consul_mgr", groupStr, old)
	g1Fresh := insert("g1-fresh", "consul_mgr", groupStr, now)
	g2Stale := insert("g2-stale", "consul_mgr", groupStr+"-other", old)
	manual := insert("manual", "manual", "", old)
	g1Online := insert("g1-online", "consul_mgr", groupStr, old)
	if _, err := db.Exec(`UPDATE hosts SET agent_status='online' WHERE id=$1`, g1Online); err != nil {
		t.Fatalf("标记在线主机失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE ip LIKE $1`, prefix+"%") })

	deleted, err := syncer.cleanupMissingMgrHosts(ctx, groupID, now)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("应只清理当前服务组 1 台，实际 %d", deleted)
	}
	exists := func(id int64) bool {
		var n int
		db.QueryRow(`SELECT COUNT(1) FROM hosts WHERE id=$1`, id).Scan(&n)
		return n > 0
	}
	if exists(g1Stale) {
		t.Fatal("当前服务组过期主机应被删除")
	}
	for name, id := range map[string]int64{"fresh": g1Fresh, "other-group": g2Stale, "manual": manual, "online": g1Online} {
		if !exists(id) {
			t.Fatalf("%s 主机不应被清理", name)
		}
	}
}

// TestCleanupMissingMgrServiceHosts 验证按服务名称清理只影响该服务范围。
func TestCleanupMissingMgrServiceHosts(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 consul_mgr 按服务清理集成测试")
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
	syncer := NewSyncer(db)
	prefix := fmt.Sprintf("mgr-svc-cleanup-%d", time.Now().UnixNano())
	groupID := int64(900000000 + time.Now().UnixNano()%100000000)
	groupStr := fmt.Sprintf("%d", groupID)
	var existing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE source='consul_mgr' AND source_group=$1`, groupStr).Scan(&existing); err != nil {
		t.Fatalf("检查现有主机失败: %v", err)
	}
	if existing > 0 {
		t.Skip("测试服务组已有主机，跳过破坏性清理测试")
	}
	now := time.Now()
	old := now.Add(-time.Hour)
	insert := func(tag, group string, services []string, synced time.Time) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRow(`
			INSERT INTO hosts (ip, hostname, tenant_id, source, source_group, source_services, last_synced_at)
			VALUES ($1, $2, 1, 'consul_mgr', $3, $4, $5) RETURNING id`,
			prefix+"-"+tag, tag, group, pq.Array(services), synced).Scan(&id); err != nil {
			t.Fatalf("插入主机 %s 失败: %v", tag, err)
		}
		return id
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE ip LIKE $1`, prefix+"%") })
	svcStale := insert("svc-stale", groupStr, []string{"selfnode_exporter"}, old)
	svcFresh := insert("svc-fresh", groupStr, []string{"selfnode_exporter"}, now)
	otherSvc := insert("other-svc", groupStr, []string{"snmp_exporter"}, old)
	otherGroup := insert("other-group", groupStr+"-other", []string{"selfnode_exporter"}, old)
	legacy := insert("legacy", groupStr, []string{}, old)
	svcOnline := insert("svc-online", groupStr, []string{"selfnode_exporter"}, old)
	if _, err := db.Exec(`UPDATE hosts SET agent_status='online' WHERE id=$1`, svcOnline); err != nil {
		t.Fatalf("标记在线主机失败: %v", err)
	}

	deleted, err := syncer.cleanupMissingMgrServiceHosts(ctx, groupID, "selfnode_exporter", now)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("应只清理该服务范围内 1 台，实际 %d", deleted)
	}
	exists := func(id int64) bool {
		var n int
		db.QueryRow(`SELECT COUNT(1) FROM hosts WHERE id=$1`, id).Scan(&n)
		return n > 0
	}
	if exists(svcStale) {
		t.Fatal("该服务范围内过期主机应被删除")
	}
	for name, id := range map[string]int64{
		"fresh": svcFresh, "other-service": otherSvc,
		"other-group": otherGroup, "legacy-no-services": legacy, "online": svcOnline,
	} {
		if !exists(id) {
			t.Fatalf("%s 主机不应被清理", name)
		}
	}
}
