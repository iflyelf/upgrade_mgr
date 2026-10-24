package sync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/consul"
)

func TestFetchServicesBoundedLimitsConcurrencyAndKeepsOrder(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f"}
	var active, maxActive int32
	results := fetchServicesBounded(context.Background(), names, 2,
		func(name string) ([]consul.ServiceInstance, error) {
			n := atomic.AddInt32(&active, 1)
			for {
				old := atomic.LoadInt32(&maxActive)
				if n <= old || atomic.CompareAndSwapInt32(&maxActive, old, n) {
					break
				}
			}
			// 让前面的任务更慢，强制完成顺序与输入顺序不同。
			if name == "a" {
				time.Sleep(30 * time.Millisecond)
			} else {
				time.Sleep(5 * time.Millisecond)
			}
			atomic.AddInt32(&active, -1)
			return []consul.ServiceInstance{{ServiceName: name}}, nil
		})
	if maxActive > 2 {
		t.Fatalf("最大并发=%d，超过配置 2", maxActive)
	}
	if len(results) != len(names) {
		t.Fatalf("结果数=%d, want %d", len(results), len(names))
	}
	for i, result := range results {
		if result.name != names[i] {
			t.Fatalf("结果顺序不稳定: index=%d got=%s want=%s", i, result.name, names[i])
		}
	}
}
