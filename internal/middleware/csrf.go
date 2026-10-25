package middleware

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/corsx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
)

// CSRFGuard 为浏览器发起的写请求增加 Origin/Referer 同源校验。
//
// 会话 Cookie 使用 SameSite=Lax，已经能挡住绝大多数跨站写请求；这里再做一层
// 纵深防御，避免同站子域、旧浏览器或未来 Cookie 策略调整导致 CSRF 回归。
//
// 规则：
//   - GET/HEAD/OPTIONS/TRACE 不校验（幂等请求由业务层保证无副作用）；
//   - 请求同时没有 Origin 与 Referer 时视为非浏览器客户端（Agent/curl），放行；
//   - Origin/Referer 的 host 必须与请求 Host 相同，或命中 CORS/ExternalURL 白名单；
//   - Origin: null（sandbox/跨源重定向）一律拒绝。
func CSRFGuard(originsFn func() []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
				next.ServeHTTP(w, r)
				return
			}

			raw := strings.TrimSpace(r.Header.Get("Origin"))
			if raw == "" {
				// Referer 只在上报来源页面时存在；提取其 origin 后按同一规则校验。
				raw = strings.TrimSpace(r.Header.Get("Referer"))
				if raw == "" {
					next.ServeHTTP(w, r)
					return
				}
			}
			origin := originOf(raw)
			if origin == "" {
				httpx.Fail(w, http.StatusForbidden, "跨站请求被拒绝")
				return
			}
			if sameHostOrigin(origin, r.Host) || allowedOrigin(origin, originsFn) {
				next.ServeHTTP(w, r)
				return
			}
			httpx.Fail(w, http.StatusForbidden, "跨站请求被拒绝")
		})
	}
}

// originOf 把 URL/Referer 归一化为 scheme://host；非法或非 http(s) 返回空串。
func originOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "null") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + strings.ToLower(u.Host)
}

// sameHostOrigin 判断 origin 与请求 Host 是否属于同一主机（忽略端口差异）。
//
// 忽略端口是为了兼容反向代理把 Host 改写成后端端口（如 127.0.0.1:8090）
// 而浏览器 Origin 仍是对外域名的常见部署方式。攻击者无法仅凭端口差异
// 拿到另一台主机上的浏览器 Cookie，因此该放宽不会削弱 CSRF 防护。
func sameHostOrigin(origin, requestHost string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := requestHost
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.EqualFold(u.Hostname(), strings.Trim(host, "[]"))
}

// allowedOrigin 判断 origin 是否命中显式白名单。
func allowedOrigin(origin string, originsFn func() []string) bool {
	if originsFn == nil {
		return false
	}
	return corsx.Match(originsFn(), origin)
}
