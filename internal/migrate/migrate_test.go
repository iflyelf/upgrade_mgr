package migrate

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

func TestAutoMigrate(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL 未设置，跳过测试")
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })

	// 清理测试环境
	cleanupTestDB(t, db)

	// 测试自动迁移
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("自动迁移失败: %v", err)
	}

	// 验证 schema_migrations 表存在
	var exists bool
	err = db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables 
			WHERE table_name = 'schema_migrations'
		)
	`).Scan(&exists)
	if err != nil {
		t.Fatalf("查询 schema_migrations 表失败: %v", err)
	}
	if !exists {
		t.Fatal("schema_migrations 表不存在")
	}

	// 验证已应用的迁移
	var version int
	err = db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	if err != nil {
		t.Fatalf("查询版本失败: %v", err)
	}
	if version < 1 {
		t.Fatalf("期望版本 >= 1，实际: %d", version)
	}
	t.Logf("✅ 当前数据库版本: %d", version)

	// 验证核心表存在
	// 迁移 005 已将 users 统一进 local_users，故断言 local_users 而非 users。
	tables := []string{"local_users", "hosts", "agents", "components", "artifacts", "upgrade_tasks", "tenants"}
	for _, table := range tables {
		err = db.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables 
				WHERE table_name = $1
			)
		`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("查询表 %s 失败: %v", table, err)
		}
		if !exists {
			t.Fatalf("表 %s 不存在", table)
		}
	}
	t.Logf("✅ 所有核心表已创建")

	// 迁移 005 应删除孤立的 users 表（与认证表 local_users 割裂，曾导致 RBAC 全链路失效）
	var legacyUsers bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='users')`).Scan(&legacyUsers); err != nil {
		t.Fatalf("查询 users 表失败: %v", err)
	}
	if legacyUsers {
		t.Fatal("孤立的 users 表应已被迁移 005 删除")
	}
	t.Logf("✅ 孤立 users 表已清理，用户数据统一在 local_users")

	// 租户字段必须存在于 local_users（真隔离前提）
	for _, col := range []string{"tenant_id", "roles"} {
		var ok bool
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='local_users' AND column_name=$1)`, col).Scan(&ok); err != nil {
			t.Fatalf("查询列 %s 失败: %v", col, err)
		}
		if !ok {
			t.Fatalf("local_users 缺少列 %s", col)
		}
	}
	t.Logf("✅ local_users 已具备 tenant_id / roles")

	// 测试重复执行迁移（幂等性）
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	t.Logf("✅ 重复迁移测试通过（幂等性）")
}

func TestMigratorStatus(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL 未设置，跳过测试")
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })

	m, err := New(db)
	if err != nil {
		t.Fatalf("创建 Migrator 失败: %v", err)
	}

	if err := m.Status(context.Background()); err != nil {
		t.Fatalf("获取状态失败: %v", err)
	}
}

func cleanupTestDB(t *testing.T, db *sql.DB) {
	// 清理测试数据（保留表结构供验证）
	// 实际测试中可选择 DROP 所有表重新测试
	t.Log("清理测试环境...")
}
