package artifact

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestUploadLimiterRaiseWakesWaiter 调大上限应立即唤醒等待者，不需要重启。
func TestUploadLimiterRaiseWakesWaiter(t *testing.T) {
	l := newUploadLimiter(1)
	release, err := l.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	got := make(chan error, 1)
	go func() {
		rel, err := l.acquire(context.Background())
		if err == nil {
			rel()
		}
		got <- err
	}()

	// 让等待者先进入等待状态，再调大上限。
	time.Sleep(50 * time.Millisecond)
	l.setLimit(2)

	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("调大上限后等待者应被唤醒: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("调大上限后等待者未被唤醒")
	}
}

// TestUploadLimiterConcurrentSetLimit 并发获取/释放与动态调大调小时，
// 不得出现数据竞争、死锁或槽位泄漏。
func TestUploadLimiterConcurrentSetLimit(t *testing.T) {
	l := newUploadLimiter(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				rel, err := l.acquire(ctx)
				if err != nil {
					return
				}
				time.Sleep(time.Millisecond)
				rel()
			}
		}()
	}

	for i := 0; i < 100; i++ {
		l.setLimit(1 + i%4)
		time.Sleep(time.Millisecond)
	}
	cancel()
	wg.Wait()

	l.mu.Lock()
	active, waiters := l.active, len(l.waiters)
	l.mu.Unlock()
	if active != 0 || waiters != 0 {
		t.Fatalf("所有请求结束后不应残留槽位: active=%d waiters=%d", active, waiters)
	}
}
