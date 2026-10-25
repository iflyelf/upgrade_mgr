package artifact

import (
	"context"
	"sync"
)

// uploadWaiter 一个等待上传槽位的请求。
type uploadWaiter struct {
	ch chan struct{}
}

// uploadLimiter 可动态调整上限的上传限流器。
//
// 与“固定容量 channel”不同，SetLimit 可以安全地在线调大/调小：
//   - 调大时立即唤醒等待者；
//   - 调小时不打断已在执行的上传，但在 active 降到新上限前不再放行新请求。
//
// 所有状态由 mu 保护，AcquireUploadSlot/SetUploadConcurrency 可在运行期并发调用。
type uploadLimiter struct {
	mu      sync.Mutex
	limit   int
	active  int
	waiters map[*uploadWaiter]struct{}
}

func newUploadLimiter(limit int) *uploadLimiter {
	if limit <= 0 {
		limit = defaultArtifactUploadConcurrency
	}
	if limit > maxArtifactUploadConcurrency {
		limit = maxArtifactUploadConcurrency
	}
	return &uploadLimiter{limit: limit, waiters: map[*uploadWaiter]struct{}{}}
}

// acquire 获取一个槽位；返回的释放函数可重复调用。
func (l *uploadLimiter) acquire(ctx context.Context) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	if l.active < l.limit {
		l.active++
		l.mu.Unlock()
		return l.releaseOnce(), nil
	}
	w := &uploadWaiter{ch: make(chan struct{}, 1)}
	l.waiters[w] = struct{}{}
	l.mu.Unlock()

	select {
	case <-w.ch:
		if err := ctx.Err(); err != nil {
			l.release()
			return nil, err
		}
		return l.releaseOnce(), nil
	case <-ctx.Done():
		l.mu.Lock()
		if _, waiting := l.waiters[w]; waiting {
			delete(l.waiters, w)
			l.mu.Unlock()
			return nil, ctx.Err()
		}
		l.mu.Unlock()
		// 已经在这期间被唤醒并占用了 active 名额，归还后再返回取消。
		l.release()
		return nil, ctx.Err()
	}
}

func (l *uploadLimiter) releaseOnce() func() {
	var once sync.Once
	return func() { once.Do(l.release) }
}

func (l *uploadLimiter) release() {
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	l.wakeLocked()
	l.mu.Unlock()
}

// setLimit 动态调整上限；调大时唤醒等待者，调小时等待 active 自然下降。
func (l *uploadLimiter) setLimit(n int) {
	if l == nil {
		return
	}
	if n <= 0 {
		n = defaultArtifactUploadConcurrency
	}
	if n > maxArtifactUploadConcurrency {
		n = maxArtifactUploadConcurrency
	}
	l.mu.Lock()
	l.limit = n
	l.wakeLocked()
	l.mu.Unlock()
}

func (l *uploadLimiter) wakeLocked() {
	for l.active < l.limit && len(l.waiters) > 0 {
		var next *uploadWaiter
		for w := range l.waiters {
			next = w
			break
		}
		delete(l.waiters, next)
		l.active++
		next.ch <- struct{}{} // 缓冲 1，不会阻塞
	}
}
