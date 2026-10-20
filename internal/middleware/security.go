package middleware

import "net/http"

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
		// CSP: 允许同源脚本/样式（SPA 常含 inline style）；img/font 允许 data: 与 self。
		// 生产环境建议收紧 unsafe-inline，改为 nonce 或 hash。
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
