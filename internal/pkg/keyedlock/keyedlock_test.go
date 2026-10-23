package keyedlock

import (
	"sync"
	"testing"
	"time"
)

// TestMutualExclusion 同一 key 串行、不同 key 并行。
func TestMutualExclusion(t *testing.T) {
	l := New()
	var mu sync.Mutex
	inside := 0
	maxInside := 0

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := l.Lock("same")
			defer unlock()
			mu.Lock()
			inside++
			if inside > maxInside {
				maxInside = inside
			}
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("同一 key 不应并发进入，实际最大并发 %d", maxInside)
	}
}

// TestDifferentKeysParallel 不同 key 不应互相阻塞。
func TestDifferentKeysParallel(t *testing.T) {
	l := New()
	unlockA := l.Lock("a")
	defer unlockA()

	done := make(chan struct{})
	go func() {
		unlockB := l.Lock("b")
		unlockB()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("不同 key 被阻塞")
	}
}

// TestNoLeak 用完的 key 会从映射中移除，避免内存泄漏。
func TestNoLeak(t *testing.T) {
	l := New()
	for i := 0; i < 100; i++ {
		unlock := l.Lock("k")
		unlock()
	}
	if got := l.Size(); got != 0 {
		t.Fatalf("应清理空 key，实际剩余 %d", got)
	}
}

// TestDoubleUnlockSafe 解锁函数重复调用不应 panic 或重复减少引用。
func TestDoubleUnlockSafe(t *testing.T) {
	l := New()
	unlock := l.Lock("x")
	unlock()
	unlock() // 应为空操作
	if got := l.Size(); got != 0 {
		t.Fatalf("重复解锁后应为空，实际 %d", got)
	}
	// 仍能再次获取
	unlock2 := l.Lock("x")
	unlock2()
}

// TestConcurrentUnlock 并发获取/释放不产生数据竞争（配合 -race）。
func TestConcurrentUnlock(t *testing.T) {
	l := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "k" + string(rune('a'+n%5))
			unlock := l.Lock(key)
			unlock()
		}(i)
	}
	wg.Wait()
	if got := l.Size(); got != 0 {
		t.Fatalf("全部释放后应清空，实际 %d", got)
	}
}
