package setting

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/migrate"
)

// TestApplyIsAtomic 批量保存必须整体成功或整体回滚：
// 第二项写库失败时，第一项不能已经落库，也不能已经写回内存配置。
func TestApplyIsAtomic(t *testing.T) {
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
		t.Fatal(err)
	}

	sfx := time.Now().UnixNano()
	keyA := fmt.Sprintf("test.atomic.a.%d", sfx)
	keyB := fmt.Sprintf("test.atomic.b.%d", sfx)
	if _, err := db.Exec(`INSERT INTO app_settings (key,value)
		VALUES ($1,'old-a'),($2,'old-b')
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`, keyA, keyB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM app_settings WHERE key IN ($1,$2)`, keyA, keyB)
	})

	// 另一个连接锁住第二项所在行，使批量事务在写第二项时阻塞并最终超时。
	lockDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lockDB.Close() })
	lockTx, err := lockDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lockTx.Rollback() })
	var locked string
	if err := lockTx.QueryRow(`SELECT key FROM app_settings WHERE key=$1 FOR UPDATE`, keyB).Scan(&locked); err != nil {
		t.Fatal(err)
	}

	var memoryA, memoryB string
	svc := &Service{db: db, cfg: config.Default(), items: map[string]Item{
		keyA: {Key: keyA,
			Get: func(*config.Config) string { return memoryA },
			Set: func(_ *config.Config, v string) { memoryA = v }},
		keyB: {Key: keyB,
			Get: func(*config.Config) string { return memoryB },
			Set: func(_ *config.Config, v string) { memoryB = v }},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := svc.Apply(ctx, map[string]string{keyA: "new-a", keyB: "new-b"}); err == nil {
		t.Fatal("第二项行被锁住时应因超时返回错误")
	}

	var gotA string
	if err := db.QueryRow(`SELECT value FROM app_settings WHERE key=$1`, keyA).Scan(&gotA); err != nil {
		t.Fatal(err)
	}
	if gotA != "old-a" {
		t.Fatalf("事务应整体回滚，第一项仍应为 old-a，实际 %q", gotA)
	}
	if memoryA != "" || memoryB != "" {
		t.Fatalf("失败时不应写回内存配置: memoryA=%q memoryB=%q", memoryA, memoryB)
	}

	// 释放行锁后同一批设置应成功，且两项同时写回。
	if err := lockTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	applied, err := svc.Apply(context.Background(), map[string]string{keyA: "new-a", keyB: "new-b"})
	if err != nil {
		t.Fatalf("释放行锁后保存应成功: %v", err)
	}
	if len(applied) != 2 {
		t.Fatalf("applied = %v, want 2 项", applied)
	}
	if memoryA != "new-a" || memoryB != "new-b" {
		t.Fatalf("成功时两项应同时写回: memoryA=%q memoryB=%q", memoryA, memoryB)
	}
}
