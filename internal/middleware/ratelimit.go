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
