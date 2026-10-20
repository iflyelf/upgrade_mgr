// Package middleware 提供 HTTP 中间件：认证、管理员校验、访问日志。
package middleware

import (
	"log"
	"net/http"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
)

// RequireAuth 校验登录；通过则把 User 放入 context。
func RequireAuth(p auth.Provider, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := p.Authenticate(r)
		if err != nil || u == nil {
			httpx.Fail(w, http.StatusUnauthorized, "未认证，请先登录")
			return
		}
		next(w, r.WithContext(auth.WithUser(r.Context(), u)))
	}
}

// RequireAdmin 在 RequireAuth 之后使用，要求管理员。
func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || !u.IsAdmin {
			httpx.Fail(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		next(w, r)
	}
}

// RequireSuperAdmin 要求超级管理员（可修改全局配置）。
func RequireSuperAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || !u.IsSuperAdmin() {
			httpx.Fail(w, http.StatusForbidden, "需要超级管理员权限")
			return
		}
		next(w, r)
	}
}

// AccessLog 简单访问日志。
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/health" {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
