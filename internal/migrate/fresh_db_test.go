package migrate

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

// TestFreshDatabaseMigrate 用一个全新空库验证迁移可从零跑通（防止迁移间冲突）。
func TestFreshDatabaseMigrate(t *testing.T) {
	dsn := os.Getenv("FRESH_DSN")
	if dsn == "" {
		t.Skip("未设置 FRESH_DSN")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("ping 失败: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("❌ 全新库迁移失败: %v", err)
	}
	t.Log("✅ 全新库迁移成功")
}
