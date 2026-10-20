package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
)

// MetricsAuth 保护 /metrics。
//
// token 非空：要求 Authorization: Bearer <token>（常量时间比较），供 Prometheus 抓取；
// token 为空：回退为登录会话认证（RequireAuth）。
func MetricsAuth(token string, p auth.Provider, next http.HandlerFunc) http.HandlerFunc {
	if token == "" {
		return RequireAuth(p, next)
	}
	want := []byte(token)
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		got := []byte(strings.TrimSpace(h[len(prefix):]))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		next(w, r)
	}
}

// LimitBody 为请求体设置上限（http.MaxBytesReader），超限时读取返回错误。
// exempt 中的 "METHOD /path" 精确匹配项不受限（如制品上传，由 handler 自行限制）。
func LimitBody(max int64, exempt ...string) func(http.Handler) http.Handler {
	skip := make(map[string]struct{}, len(exempt))
	for _, e := range exempt {
		skip[e] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := skip[r.Method+" "+r.URL.Path]; !ok && r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}
