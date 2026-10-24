package sync

import (
	"context"
	"sync"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/consul"
)

// serviceFetchResult 单个服务实例查询结果。
type serviceFetchResult struct {
	index     int
	name      string
	instances []consul.ServiceInstance
	err       error
}

// fetchServicesBounded 以固定并发度查询多个 Consul 服务。
//
// 使用 worker pool 而非「每服务一个 goroutine + 信号量」，服务数上万时也只创建
// concurrency 个 goroutine；结果通过缓冲 channel 汇总，不共享写状态。
func fetchServicesBounded(ctx context.Context, names []string, concurrency int,
	fetch func(string) ([]consul.ServiceInstance, error)) []serviceFetchResult {
	if len(names) == 0 {
		return nil
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(names) {
		concurrency = len(names)
	}

	type serviceJob struct {
		index int
		name  string
	}
	jobs := make(chan serviceJob)
	results := make(chan serviceFetchResult, len(names))
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}
				instances, err := fetch(job.name)
				results <- serviceFetchResult{index: job.index, name: job.name, instances: instances, err: err}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for index, name := range names {
			select {
			case <-ctx.Done():
				return
			case jobs <- serviceJob{index: index, name: name}:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	// 按输入下标放回结果：网络请求仍并发，但后续聚合顺序与串行实现一致。
	ordered := make([]serviceFetchResult, len(names))
	received := make([]bool, len(names))
	for result := range results {
		ordered[result.index] = result
		received[result.index] = true
	}
	out := make([]serviceFetchResult, 0, len(names))
	for i, result := range ordered {
		if received[i] {
			out = append(out, result)
		}
	}
	return out
}
