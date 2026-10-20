package migrate

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	_ "github.com/lib/pq"
)

// TestConcurrentMigrate 模拟多实例并发启动，验证 advisory lock 保证迁移只执行一次、无冲突。
func TestConcurrentMigrate(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过并发迁移测试")
	}

	// 清理，回到全新状态
	cleanup := func() {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
	}
	cleanup()
	defer cleanup()

	const instances = 8
	var wg sync.WaitGroup
	errs := make([]error, instances)

	// 每个实例独立连接池，模拟不同进程并发启动
	for i := 0; i < instances; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			db, err := sql.Open("postgres", dsn)
			if err != nil {
				errs[idx] = err
				return
			}
			defer db.Close()
			errs[idx] = AutoMigrate(db)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("实例 %d 迁移失败: %v", i, err)
		}
	}

	// 验证最终版本正确且迁移记录无重复
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("查询迁移记录失败: %v", err)
	}
	m, _ := New(db)
	if count != len(m.migrations) {
		t.Fatalf("迁移记录数=%d，期望=%d（可能有重复或缺失）", count, len(m.migrations))
	}
	t.Logf("✅ %d 实例并发启动，迁移记录=%d 条，无重复无冲突", instances, count)
}
