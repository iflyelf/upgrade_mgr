package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/netutil"
)

// TestRateLimit_XFFSpoofBlocked 未信任代理时，伪造 XFF 不能绕过限流。
func TestRateLimit_XFFSpoofBlocked(t *testing.T) {
	os.Unsetenv("UPGRADE_MGR_TRUST_PROXY")
	rl := NewRateLimiter(3, time.Minute)
	h := RateLimitMiddleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	allowed := 0
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("POST", "/api/agent/register", nil)
		req.RemoteAddr = "10.0.0.7:12345"
		// 攻击者每次伪造不同 XFF 试图绕过
		req.Header.Set("X-Forwarded-For", "1.2.3."+string(rune('0'+i)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == 200 {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("伪造 XFF 绕过了限流：放行 %d 次（期望 3）", allowed)
	}
	t.Logf("✅ 伪造 XFF 未绕过限流，仅放行 %d 次", allowed)
}

// TestRateLimit_TrustProxy 显式信任代理并配置受信列表时，从右往左跳过受信代理取真实客户端。
func TestRateLimit_TrustProxy(t *testing.T) {
	trusted, err := netutil.ParseTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	rl := NewRateLimiterWithProxies(1, time.Minute, true, trusted)
	defer rl.Stop()
	h := RateLimitMiddleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	ok := 0
	for _, ip := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
		req := httptest.NewRequest("POST", "/x", nil)
		req.RemoteAddr = "10.0.0.7:1"
		req.Header.Set("X-Forwarded-For", ip+", 10.0.0.1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == 200 {
			ok++
		}
	}
	if ok != 3 {
		t.Fatalf("信任代理时应按真实客户端 IP 区分，放行 %d（期望 3）", ok)
	}
}

// TestRateLimit_TrustProxyLeftSpoof 客户端在 XFF 最左侧伪造 IP 不能绕过限流。
func TestRateLimit_TrustProxyLeftSpoof(t *testing.T) {
	trusted, _ := netutil.ParseTrustedProxies([]string{"10.0.0.0/8"})
	rl := NewRateLimiterWithProxies(1, time.Minute, true, trusted)
	defer rl.Stop()
	h := RateLimitMiddleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	ok := 0
	for _, fake := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
		req := httptest.NewRequest("POST", "/x", nil)
		req.RemoteAddr = "10.0.0.7:1"
		// 真实客户端 9.9.9.9 由代理追加在右侧，左侧为伪造值
		req.Header.Set("X-Forwarded-For", fake+", 9.9.9.9")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == 200 {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("XFF 左侧伪造绕过了限流：放行 %d（期望 1）", ok)
	}
}

// TestRateLimit_StopIdempotent 重复 Stop 不 panic。
func TestRateLimit_StopIdempotent(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)
	rl.Stop()
	rl.Stop()
}

// TestRateLimit_Concurrent 并发下限流器无竞态（配合 -race）。
func TestRateLimit_Concurrent(t *testing.T) {
	rl := NewRateLimiter(100, time.Minute)
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func(n int) {
			for j := 0; j < 50; j++ {
				rl.Allow("10.0.0." + string(rune('0'+n%10)))
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	t.Log("✅ 并发调用无竞态")
}
