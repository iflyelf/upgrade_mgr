package handler

import (
	"net/http"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// CacheStatus 返回 Redis 缓存状态（管理员）。
//
// 不返回密码；只显示模式、地址、实际 DB、TTL、前缀与最近错误，便于页面排障。
func CacheStatus(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := s.GetCache()
		if c == nil {
			httpx.OK(w, map[string]any{"enabled": false, "last_error": "缓存未初始化"})
			return
		}
		st := c.Status()
		httpx.OK(w, map[string]any{
			"enabled":      st.Enabled,
			"mode":         st.Mode,
			"addrs":        st.Addrs,
			"master_name":  st.MasterName,
			"db":           st.DB,
			"requested_db": st.RequestedDB,
			"db_fallback":  st.DBFallback,
			"ttl_seconds":  st.TTLSeconds,
			"prefix":       st.Prefix,
			"last_error":   st.LastError,
		})
	}
}

// CacheTest 检查连通性与读写（SET/GET/DEL 一个临时键）。
func CacheTest(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := s.GetCache()
		if c == nil || !c.Enabled() {
			httpx.Fail(w, http.StatusServiceUnavailable, "Redis 未启用或连接失败，请检查设置")
			return
		}
		latency, err := c.Test(r.Context())
		if err != nil {
			httpx.FailErrf(w, http.StatusBadGateway, "Redis 读写失败", err)
			return
		}
		st := c.Status()
		httpx.OK(w, map[string]any{
			"ok": true, "latency_ms": latency.Milliseconds(),
			"mode": st.Mode, "db": st.DB,
		})
	}
}

// CacheFlush 清空本平台前缀下的缓存（不动同实例其它系统的键）。
func CacheFlush(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := s.GetCache()
		if c == nil || !c.Enabled() {
			httpx.Fail(w, http.StatusServiceUnavailable, "Redis 未启用或连接失败")
			return
		}
		prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))
		if prefix == "" {
			prefix = "http"
		}
		deleted := c.DelPrefix(r.Context(), prefix)
		httpx.OK(w, map[string]any{"deleted": deleted, "prefix": prefix})
	}
}
