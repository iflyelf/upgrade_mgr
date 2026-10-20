package leader

import (
	"sync"
	"testing"
)

// TestSessionIDNoRace 并发读写 sessionID / isLeader，配合 -race 验证无数据竞争。
//
// 背景：Campaign 写 sessionID，Resign/Watch 读 sessionID，IsLeader 与 Watch
// 并发读写 isLeader。历史实现无同步，存在数据竞争，可导致脑裂判断错误。
func TestSessionIDNoRace(t *testing.T) {
	e := &Election{key: "k"}

	var wg sync.WaitGroup
	const n = 200

	// 写 sessionID（模拟 Campaign 重新竞选）
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			e.mu.Lock()
			e.sessionID = "sid"
			e.mu.Unlock()
		}
	}()

	// 读 sessionID（模拟 Resign / Watch）
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_ = e.currentSession()
		}
	}()

	// 并发读写 isLeader（模拟 Watch 与 IsLeader）
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			e.isLeader.Store(i%2 == 0)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_ = e.IsLeader()
		}
	}()

	wg.Wait()
	t.Log("✅ sessionID / isLeader 并发访问无竞态")
}
