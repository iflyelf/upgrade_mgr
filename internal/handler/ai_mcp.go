package handler

import (
	"net/http"
	"sync"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/mcp"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// MCPServer MCP 服务端点：POST /mcp（Streamable HTTP over JSON-RPC 2.0）。
//
// 使用 Bearer AI token 认证；只读工具可直接调用，写工具需要令牌与全局配置同时允许。
func MCPServer(s *svc.ServiceContext) http.HandlerFunc {
	limiter := newTokenRateLimiter()
	return func(w http.ResponseWriter, r *http.Request) {
		snap := s.Settings.Snapshot()
		if !snap.AI.MCPEnabled {
			http.Error(w, "MCP 服务未启用", http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		// Bearer token 认证
		token := extractBearer(r.Header.Get("Authorization"))
		if token == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="MCP"`)
			http.Error(w, "需要 AI 访问令牌", http.StatusUnauthorized)
			return
		}
		u, t, err := s.AIToken.Authenticate(r.Context(), token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="MCP"`)
			http.Error(w, "令牌无效或已过期", http.StatusUnauthorized)
			return
		}

		// 限流（每令牌每分钟）
		if snap.AI.MCPRateLimit > 0 && !limiter.allow(t.ID, snap.AI.MCPRateLimit) {
			httpx.Fail(w, http.StatusTooManyRequests, "请求过于频繁")
			return
		}

		// 更新最近使用时间
		interval := parseDur(snap.AI.TokenTouchInterval)
		if interval > 0 {
			s.AIToken.Touch(t.ID, clientIP(s, r), interval)
		}

		// 应用 MCP 专用 body 大小限制（覆盖全局 1MB 限制）
		if snap.AI.MCPMaxBodyBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, int64(snap.AI.MCPMaxBodyBytes))
		}

		// Provider：按当前令牌身份动态构造工具
		provider := &AIToolsProvider{
			svc:      s,
			user:     u,
			token:    t,
			request:  r,
			snapshot: snap,
			channel:  channelMCP,
		}
		server := &mcp.Server{Provider: provider}
		server.ServeHTTP(w, r)
	}
}

// tokenRateLimiter 简单的固定窗口限流器（每令牌每分钟）。
type tokenRateLimiter struct {
	mu      sync.Mutex
	buckets map[int64]*bucket
}

type bucket struct {
	count       int
	windowStart time.Time
}

func newTokenRateLimiter() *tokenRateLimiter {
	return &tokenRateLimiter{buckets: make(map[int64]*bucket)}
}

func (r *tokenRateLimiter) allow(tokenID int64, max int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b, ok := r.buckets[tokenID]
	if !ok || now.Sub(b.windowStart) >= time.Minute {
		r.buckets[tokenID] = &bucket{count: 1, windowStart: now}
		r.cleanup(now)
		return true
	}
	if b.count >= max {
		return false
	}
	b.count++
	return true
}

func (r *tokenRateLimiter) cleanup(now time.Time) {
	if len(r.buckets) < 1000 {
		return
	}
	for id, b := range r.buckets {
		if now.Sub(b.windowStart) > 2*time.Minute {
			delete(r.buckets, id)
		}
	}
}
