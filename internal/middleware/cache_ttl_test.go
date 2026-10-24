package middleware

import (
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/cache"
)

// TestTTLFor 校验 /api/hosts 使用独立 TTL，其余端点沿用通用 TTL。
func TestTTLFor(t *testing.T) {
	c := cache.New(cache.Options{
		Enabled:  false,
		TTL:      10 * time.Second,
		HostsTTL: 2 * time.Minute,
	})
	if got := ttlFor(c, "/api/hosts"); got != 2*time.Minute {
		t.Fatalf("/api/hosts ttl=%s, want 2m", got)
	}
	for _, p := range []string{"/api/tasks", "/api/components", "/api/settings"} {
		if got := ttlFor(c, p); got != 10*time.Second {
			t.Fatalf("%s ttl=%s, want 10s", p, got)
		}
	}
}

// TestTTLForWithoutHostsTTL 未单独配置时 /api/hosts 与其它端点一致。
func TestTTLForWithoutHostsTTL(t *testing.T) {
	c := cache.New(cache.Options{Enabled: false, TTL: 15 * time.Second})
	if got := ttlFor(c, "/api/hosts"); got != 15*time.Second {
		t.Fatalf("/api/hosts ttl=%s, want 15s", got)
	}
}
