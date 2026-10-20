package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// Health 存活检查（无需认证）：进程能响应即可。
func Health() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"upgrade_mgr"}`))
	}
}

// Ready 就绪检查（无需认证）：数据库可用才接收流量。
//
// 滚动重启时 Kubernetes 用本接口判断新副本是否可接管；Consul 是可选依赖，
// 不参与就绪判断。
func Ready(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.DB.PingContext(ctx); err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready","database":"unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready","service":"upgrade_mgr"}`))
	}
}
