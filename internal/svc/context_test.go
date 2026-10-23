package svc

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/config"
)

// TestEnsureSessionSecretPersists 验证空 SESSION_SECRET 生成一次并持久化，
// 第二个副本/下一次启动读到同一值；显式长密钥不被覆盖。
//
// 使用独立 schema，避免影响 public.app_settings。
func TestEnsureSessionSecretPersists(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过会话密钥集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	// 单连接保证 SET search_path 对后续 ensureSessionSecret 生效。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	schema := fmt.Sprintf("svc_secret_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("创建 schema 失败: %v", err)
	}
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatalf("设置 search_path 失败: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE app_settings (
			key VARCHAR(128) PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`); err != nil {
		t.Fatalf("创建 app_settings 失败: %v", err)
	}

	c1 := &config.Config{}
	ensureSessionSecret(db, c1)
	if len(c1.Server.SessionSecret) < 16 {
		t.Fatalf("自动生成密钥失败，长度 %d", len(c1.Server.SessionSecret))
	}

	c2 := &config.Config{}
	ensureSessionSecret(db, c2)
	if c2.Server.SessionSecret != c1.Server.SessionSecret {
		t.Fatal("第二个实例未读到同一会话密钥")
	}

	// 过短的显式密钥应回退到数据库共享密钥。
	c3 := &config.Config{}
	c3.Server.SessionSecret = "short"
	ensureSessionSecret(db, c3)
	if c3.Server.SessionSecret != c1.Server.SessionSecret {
		t.Fatal("过短密钥未回退到数据库密钥")
	}

	// 显式 32+ 字节密钥优先，不应被数据库值覆盖。
	explicit := "explicit-session-secret-32-bytes!"
	c4 := &config.Config{}
	c4.Server.SessionSecret = explicit
	ensureSessionSecret(db, c4)
	if c4.Server.SessionSecret != explicit {
		t.Fatal("显式密钥被数据库值覆盖")
	}
}
