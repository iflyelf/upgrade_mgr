package pglock

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// TestTryLock 需要真实 PostgreSQL；未设置 DATABASE_URL 时跳过。
func TestTryLock(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 advisory lock 集成测试")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const key int64 = 0x75706772616465ff
	l1, ok, err := Try(ctx, db, key)
	if err != nil {
		t.Fatalf("第一次抢锁失败: %v", err)
	}
	if !ok {
		t.Fatal("第一次抢锁应成功")
	}

	l2, ok, err := Try(ctx, db, key)
	if err != nil {
		t.Fatalf("第二次抢锁返回错误: %v", err)
	}
	if ok {
		l2.Unlock()
		t.Fatal("同一 key 的第二次抢锁应失败")
	}

	l1.Unlock()

	l3, ok, err := Try(ctx, db, key)
	if err != nil {
		t.Fatalf("解锁后抢锁失败: %v", err)
	}
	if !ok {
		t.Fatal("解锁后应能重新抢到锁")
	}
	l3.Unlock()
}
