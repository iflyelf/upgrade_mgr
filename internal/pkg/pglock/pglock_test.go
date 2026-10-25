package pglock

import (
	"context"
	"database/sql"
	"fmt"
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
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
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
	if l1.Conn() == nil {
		t.Fatal("持锁期间应能拿到专用连接")
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

// TestKeyFromString 跨进程/跨副本必须得到相同的 int64 key。
func TestKeyFromString(t *testing.T) {
	a := KeyFromString("artifact|1|v1|amd64|linux")
	b := KeyFromString("artifact|1|v1|amd64|linux")
	if a != b {
		t.Fatalf("相同输入必须得到相同 key: %d != %d", a, b)
	}
	if KeyFromString("artifact|1|v1|arm64|linux") == a {
		t.Fatal("不同输入不应发生碰撞")
	}
}

// TestTryManySharedExclusive 验证“组件共享锁 + 制品独占锁”：
// 不同制品可并行，同一制品串行；组件独占锁会阻塞所有共享锁。
func TestTryManySharedExclusive(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	component := KeyFromString(fmt.Sprintf("test-component-%d", time.Now().UnixNano()))
	idA := KeyFromString("test-artifact-A")
	idB := KeyFromString("test-artifact-B")

	l1, ok, err := TryMany(ctx, db, []LockSpec{
		{Key: component, Shared: true},
		{Key: idA},
	})
	if err != nil || !ok {
		t.Fatalf("第一个上传锁应成功: ok=%v err=%v", ok, err)
	}
	defer l1.Unlock()

	l2, ok, err := TryMany(ctx, db, []LockSpec{
		{Key: component, Shared: true},
		{Key: idB},
	})
	if err != nil || !ok {
		t.Fatalf("不同制品的上传锁应并行成功: ok=%v err=%v", ok, err)
	}
	defer l2.Unlock()

	l3, ok, err := TryMany(ctx, db, []LockSpec{
		{Key: component, Shared: true},
		{Key: idA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		l3.Unlock()
		t.Fatal("同一制品的第二个上传锁应失败")
	}

	// 组件独占锁（删除/Purge）在任一共享上传锁存在时必须失败。
	l4, ok, err := TryMany(ctx, db, []LockSpec{{Key: component}})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		l4.Unlock()
		t.Fatal("共享上传锁存在时组件独占锁不应成功")
	}

	// 释放共享锁后才能取得组件独占锁。
	l1.Unlock()
	l2.Unlock()
	l5, ok, err := TryMany(ctx, db, []LockSpec{{Key: component}})
	if err != nil || !ok {
		t.Fatalf("共享锁释放后组件独占锁应成功: ok=%v err=%v", ok, err)
	}
	l5.Unlock()
}
