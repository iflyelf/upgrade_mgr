package middleware

import (
	"net/http"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/corsx"
)

// CORS 为跨域浏览器请求增加严格 Origin 白名单支持。
//
// originsFn 每次请求读取当前配置，因此设置页保存后无需重启即可生效。
// 白名单为空时完全不添加 CORS 头；非白名单 Origin 的实际请求正常透传，
// 由浏览器阻止读取响应；非白名单预检直接返回 403。
func CORS(originsFn func() []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			var origins []string
			if originsFn != nil {
				origins = originsFn()
			}
			allowed := corsx.Match(origins, origin)
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if !allowed {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				setCORSHeaders(w, origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With, X-Service-Token")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if allowed {
				setCORSHeaders(w, origin)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func setCORSHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Add("Vary", "Origin")
}
