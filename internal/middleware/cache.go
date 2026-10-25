package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/cache"
)

// 可缓存的只读列表 API：页面轮询/频繁切换时直接命中 Redis，减少 PostgreSQL 压力。
//
// 只缓存「列表/统计」端点，不缓存详情（任务详情/主机历史等需要准实时）。
// 只缓存 JSON 200 响应；带 Set-Cookie 的响应不缓存（登录态变化）。
// 键包含会话 cookie 哈希，天然按用户隔离，不会串租户数据。
var cacheablePaths = map[string]bool{
	"/api/hosts":               true,
	"/api/agents/stats":        true,
	"/api/components":          true,
	"/api/artifacts":           true,
	"/api/artifacts/paged":     true,
	"/api/tasks":               true,
	"/api/tasks/creators":      true,
	"/api/audit-logs":          true,
	"/api/settings":            true,
	"/api/quickstart":          true,
	"/api/my/permissions":      true,
	"/api/users":               true,
	"/api/users/fields":        true,
	"/api/host-field-mappings": true,
	"/api/roles":               true,
	"/api/teams":               true,
	"/api/service-groups":      true,
	"/api/host-groups":         true,
	"/api/org/departments":     true,
	"/api/flyiam/users":        true,
	"/api/external/binaries":   true,
	"/api/tenants":             true,
}

// cacheablePath 判断是否属于可缓存列表端点（排除详情、写操作与 Agent 高频接口）。
func cacheablePath(path string) bool {
	return cacheablePaths[path]
}

// ttlFor 返回某端点的缓存时长。
//
// /api/hosts 在几万台规模下是最重的查询，支持单独配置更长的 TTL
// （redis.hosts_ttl）；其余端点沿用通用 TTL。
// 两者都依赖「任意写请求清空 http 前缀」来保证修改立即可见，
// 所以放长主机列表 TTL 不会让页面操作看起来没生效。
func ttlFor(c *cache.Cache, path string) time.Duration {
	if path == "/api/hosts" {
		return c.HostsTTL()
	}
	return c.TTL()
}

// CachedResponse 缓存的响应体。
type CachedResponse struct {
	Status      int       `json:"status"`
	ContentType string    `json:"content_type"`
	Body        []byte    `json:"body"`
	CachedAt    time.Time `json:"cached_at"`
}

// Cache 为只读 API 增加 Redis 缓存；Redis 未启用时原样透传。
//
// provider 每次请求求值，设置页切换/重建 Redis 连接后立即生效，
// 无需重启服务。Redis 不可用时自动直连数据库。
//
// 失效策略：任何非 GET 的 /api/ 请求（排除 Agent 心跳）都会清空 HTTP 缓存前缀。
// 这比逐资源失效简单且安全——页面写操作频率低，而读操作/轮询频率高。
func Cache(provider func() *cache.Cache) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := provider()
			if c == nil || !c.Enabled() {
				next.ServeHTTP(w, r)
				return
			}
			ttl := ttlFor(c, r.URL.Path)
			// 写操作：仅在业务成功（2xx/3xx）后清空缓存。
			//
			// 不能在校验前无条件清空：缓存中间件位于认证之外，未登录/无权限的
			// 请求（401/403/404）同样会走到这里。若每次失败请求都执行 SCAN+DEL，
			// 攻击者可以低成本地反复清空缓存并给 Redis 施压。
			if r.Method != http.MethodGet && r.Method != http.MethodHead &&
				strings.HasPrefix(r.URL.Path, "/api/") &&
				!strings.HasPrefix(r.URL.Path, "/api/agent/") {
				rec := &statusRecorder{ResponseWriter: w}
				next.ServeHTTP(rec, r)
				if rec.statusCode() >= 200 && rec.statusCode() < 400 {
					// 响应已写回客户端，这里只做后台失效；给 Redis 一个独立超时，
					// 避免请求 context 已取消时缓存永远不失效。
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					c.DelPrefix(ctx, "http")
				}
				return
			}
			if r.Method != http.MethodGet || !cacheablePath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			// 会话 cookie 哈希用于按用户隔离；匿名请求共享一个桶。
			cookie := r.Header.Get("Cookie")
			sum := sha256.Sum256([]byte(cookie))
			key := c.Key("http", hex.EncodeToString(sum[:12]), r.URL.RequestURI())
			var cached CachedResponse
			if c.GetJSON(r.Context(), key, &cached) && len(cached.Body) > 0 && cached.Status == http.StatusOK {
				w.Header().Set("Content-Type", cached.ContentType)
				w.Header().Set("X-Cache", "HIT")
				w.WriteHeader(cached.Status)
				_, _ = w.Write(cached.Body)
				return
			}

			// 缓冲区记录响应；超过 2MB 不再缓存，避免大响应占用 Redis。
			w.Header().Set("X-Cache", "MISS")
			bw := &bufferedResponse{ResponseWriter: w, limit: 2 << 20, status: http.StatusOK}
			next.ServeHTTP(bw, r)
			if bw.status != http.StatusOK || bw.overflow || len(bw.buf.Bytes()) == 0 {
				return
			}
			if strings.Contains(strings.ToLower(bw.Header().Get("Set-Cookie")), "=") {
				return
			}
			ct := bw.Header().Get("Content-Type")
			if !strings.Contains(strings.ToLower(ct), "application/json") {
				return
			}
			if strings.EqualFold(bw.Header().Get("Cache-Control"), "no-store") {
				return
			}
			_ = c.SetJSON(r.Context(), key, CachedResponse{
				Status: bw.status, ContentType: ct,
				Body: append([]byte(nil), bw.buf.Bytes()...), CachedAt: time.Now(),
			}, ttl)
		})
	}
}

// statusRecorder 记录响应状态码，供写操作成功后决定是否失效缓存。
//
// 默认状态为 200：部分 handler 直接 Write 而不显式 WriteHeader。
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wrote {
		return
	}
	s.wrote = true
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if !s.wrote {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusRecorder) statusCode() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}

// Flush 保留流式响应能力。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// bufferedResponse 先写入内存，成功后再决定是否落缓存。
//
// 超限后立即切换为直写，避免大响应被完整缓冲在内存里。
type bufferedResponse struct {
	http.ResponseWriter
	buf      bytes.Buffer
	status   int
	wrote    bool
	overflow bool
	limit    int
}

func (b *bufferedResponse) WriteHeader(code int) {
	if b.wrote {
		return
	}
	b.wrote = true
	b.status = code
	b.ResponseWriter.WriteHeader(code)
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if !b.wrote {
		b.WriteHeader(http.StatusOK)
	}
	if b.overflow {
		return b.ResponseWriter.Write(p)
	}
	if b.buf.Len()+len(p) > b.limit {
		b.overflow = true
		b.buf.Reset()
		return b.ResponseWriter.Write(p)
	}
	b.buf.Write(p)
	return b.ResponseWriter.Write(p)
}

// Flush 保留流式响应能力（如 SSE/长响应）。
func (b *bufferedResponse) Flush() {
	if f, ok := b.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
