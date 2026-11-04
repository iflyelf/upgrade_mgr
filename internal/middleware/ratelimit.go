// Package middleware 费率限制：防止暴力破解和 DDoS 攻击。
package middleware

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/netutil"
)

// RateLimiter 简单的滑动窗口限流器（基于 IP）。
type RateLimiter struct {
	mu         sync.Mutex
	limits     map[string]*bucket // IP -> bucket
	maxReq     int                // 最大请求数
	window     time.Duration      // 时间窗口
	cleanInt   time.Duration      // 清理过期记录的间隔
	trustProxy bool               // 是否信任 XFF/X-Real-IP（部署在受信代理后才可开启）
	trusted    *netutil.ProxyList // 受信代理列表（nil=未配置，取 XFF 最右值）
	stopCh     chan struct{}      // 停止清理 goroutine 的信号
	stopOnce   sync.Once
}

type bucket struct {
	count     int
	resetTime time.Time
}

// NewRateLimiter 创建限流器。
// maxReq: 时间窗口内最大请求数，window: 时间窗口（如 1 分钟）。
func NewRateLimiter(maxReq int, window time.Duration) *RateLimiter {
	return NewRateLimiterWithProxy(maxReq, window, false)
}

// NewRateLimiterWithProxy 创建限流器并指定是否信任代理转发头。
func NewRateLimiterWithProxy(maxReq int, window time.Duration, trustProxy bool) *RateLimiter {
	return NewRateLimiterWithProxies(maxReq, window, trustProxy, nil)
}

// NewRateLimiterWithProxies 创建限流器，并指定受信代理列表（XFF 从右往左跳过受信代理）。
func NewRateLimiterWithProxies(maxReq int, window time.Duration, trustProxy bool, trusted *netutil.ProxyList) *RateLimiter {
	rl := &RateLimiter{
		trustProxy: trustProxy,
		trusted:    trusted,
		limits:     make(map[string]*bucket),
		maxReq:     maxReq,
		window:     window,
		cleanInt:   window * 2,
		stopCh:     make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// Stop 停止清理 goroutine（优雅关闭时调用，可重复调用）。
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() { close(rl.stopCh) })
}

// Allow 判断 IP 是否允许通过（true=放行，false=拒绝）。
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.limits[ip]
	if !ok || now.After(b.resetTime) {
		// 新 IP 或窗口已过期，重置
		rl.limits[ip] = &bucket{
			count:     1,
			resetTime: now.Add(rl.window),
		}
		return true
	}

	if b.count >= rl.maxReq {
		return false // 超过限制
	}

	b.count++
	return true
}

// cleanup 定期清理过期记录（防止内存泄漏），监听 stopCh 实现优雅退出。
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.cleanInt)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for ip, b := range rl.limits {
				if now.After(b.resetTime) {
					delete(rl.limits, ip)
				}
			}
			rl.mu.Unlock()
		}
	}
}

// DynamicRateLimiter 按运行时上限动态调整限流。
//
// 与固定 RateLimiter 不同，上限由调用方在每次请求时传入（如从设置快照读取），
// 因此页面保存新的上限后无需重启即可生效；limit<=0 表示关闭限流。
// 上限变化时会重建底层滑动窗口（窗口内计数重置），避免旧阈值残留。
type DynamicRateLimiter struct {
	mu         sync.Mutex
	cur        *RateLimiter
	curLimit   int
	window     time.Duration
	trustProxy bool
	trusted    *netutil.ProxyList
	stopped    bool
}

// NewDynamicRateLimiter 创建动态限流器。
func NewDynamicRateLimiter(window time.Duration, trustProxy bool, trusted *netutil.ProxyList) *DynamicRateLimiter {
	return &DynamicRateLimiter{window: window, trustProxy: trustProxy, trusted: trusted}
}

// Allow 按当前上限判断是否放行；limit<=0 时直接放行。
func (d *DynamicRateLimiter) Allow(ip string, limit int) bool {
	if limit <= 0 {
		return true
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return true
	}
	if d.cur == nil || d.curLimit != limit {
		if d.cur != nil {
			d.cur.Stop()
		}
		d.cur = NewRateLimiterWithProxies(limit, d.window, d.trustProxy, d.trusted)
		d.curLimit = limit
	}
	cur := d.cur
	d.mu.Unlock()
	return cur.Allow(ip)
}

// Stop 停止当前底层限流器的清理协程（可重复调用）。
func (d *DynamicRateLimiter) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	d.stopped = true
	if d.cur != nil {
		d.cur.Stop()
		d.cur = nil
	}
}

// Stopper 可停止后台清理协程的限流器（固定/动态限流器均实现）。
type Stopper interface {
	Stop()
}

// DynamicRateLimitMiddleware 返回动态限流中间件。
//
// limit 在每次请求时调用，返回当前允许的窗口内请求数；<=0 表示关闭。
// 设置页保存 server.login_rate_limit 后，内存快照立即更新，下一次请求即生效。
func DynamicRateLimitMiddleware(limiter *DynamicRateLimiter, limit func() int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := 0
			if limit != nil {
				n = limit()
			}
			ip := netutil.ClientIPWithProxies(r, limiter.trustProxy, limiter.trusted)
			if !limiter.Allow(ip, n) {
				log.Printf("⚠️ Rate limit exceeded: %s %s (IP: %s)", r.Method, r.URL.Path, ip)
				http.Error(w, "请求过于频繁，请稍后重试", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitMiddleware 返回限流中间件（包装 http.Handler）。
func RateLimitMiddleware(limiter *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := netutil.ClientIPWithProxies(r, limiter.trustProxy, limiter.trusted)
			if !limiter.Allow(ip) {
				log.Printf("⚠️ Rate limit exceeded: %s %s (IP: %s)", r.Method, r.URL.Path, ip)
				http.Error(w, "请求过于频繁，请稍后重试", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
