package middleware

import (
	"net/http"
	"strings"
)

// SecurityHeaders 添加安全响应头，防御常见 Web 攻击。
//
// - X-Frame-Options: 防点击劫持
// - X-Content-Type-Options: 防 MIME 嗅探
// - X-XSS-Protection: 浏览器 XSS 过滤（旧浏览器兼容）
// - Referrer-Policy: 控制 Referer 泄露
// - Content-Security-Policy: 限制资源加载（前端为 SPA，允许 self + unsafe-inline）
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-XSS-Protection", "1; mode=block")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// 关闭不需要的浏览器能力，降低被恶意页面利用（摄像头/麦克风/定位）的风险。
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// CSP: 允许同源脚本/样式（SPA 常含 inline style）；img/font 允许 data: 与 self。
		// object-src/base-uri/form-action 收紧到最小集合，减少 XSS/注入后的攻击面。
		// 生产环境建议进一步收紧 unsafe-inline，改为 nonce 或 hash。
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"object-src 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")
		// 仅在服务端直接终止 TLS 时下发 HSTS；反向代理终止 TLS 时由代理负责。
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		// API 响应包含登录态/主机清单等敏感数据，禁止浏览器与中间代理缓存；
		// 服务端 Redis 页面缓存不受影响（由 Cache 中间件单独控制）。
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
