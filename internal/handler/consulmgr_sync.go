package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	synclogic "github.com/iflyelf/upgrade_mgr/internal/logic/sync"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// SyncHostsFromConsulMgr 从 consul_mgr 同步主机清单（管理员）。
//
// 与 /api/hosts/sync（直连 Consul）并存：两者来源不同、锁不同，可分别触发。
// consul_mgr 覆盖多个服务组（多套 Consul），适合统一纳管的环境。
//
// group_id 查询参数可临时只同步某个服务组；缺省用设置里的 consul_mgr.group_id。
func SyncHostsFromConsulMgr(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cli := s.ConsulMgr()
		if cli == nil {
			httpx.Fail(w, http.StatusServiceUnavailable,
				"未配置 consul_mgr 地址与服务令牌，请先到「设置 → consul_mgr 对接」填写")
			return
		}

		groupID := s.ConsulMgrGroupID()
		if v := strings.TrimSpace(r.URL.Query().Get("group_id")); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				httpx.Fail(w, http.StatusBadRequest, "group_id 无效")
				return
			}
			groupID = n
		}

		res, err := s.Syncer.SyncFromConsulMgr(r.Context(), cli, groupID)
		if errors.Is(err, synclogic.ErrSyncInProgress) {
			httpx.Fail(w, http.StatusConflict, "已有同步任务正在执行，请稍后重试")
			return
		}
		if err != nil {
			auditOp(s, r, audit.ActionConsulMgrSync, "host", "", cli.Endpoint(), err)
			httpx.FailErrf(w, http.StatusBadGateway, "同步失败", err)
			return
		}

		auditOpDetails(s, r, audit.ActionConsulMgrSync, "host", "", cli.Endpoint(), nil,
			map[string]any{
				"groups": res.Groups, "hosts": res.Hosts,
				"upserted": res.Upserted, "partial": res.Partial,
			}, nil)
		httpx.OK(w, map[string]any{
			"groups": res.Groups, "hosts": res.Hosts,
			"upserted": res.Upserted, "mappings": res.Mappings,
			"failed_groups": res.FailedGroups, "partial": res.Partial,
			"duration": res.Duration.String(),
		})
	}
}

// TestConsulMgr 测试 consul_mgr 连通性与令牌（管理员）。
//
// 不返回主机明细，只回报可达性、主机/服务组数量与耗时，便于配置时自检。
func TestConsulMgr(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cli := s.ConsulMgr()
		if cli == nil {
			httpx.Fail(w, http.StatusBadRequest,
				"未配置 consul_mgr 地址与服务令牌")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		start := time.Now()
		res, err := cli.FetchHosts(ctx, s.ConsulMgrGroupID())
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			httpx.OK(w, map[string]any{
				"ok": false, "endpoint": cli.Endpoint(), "elapsed_ms": elapsed,
				"error": err.Error(),
				"hint":  "确认 consul_mgr 地址可达，且其「设置 → 服务间 API」中的令牌与此处一致",
			})
			return
		}
		httpx.OK(w, map[string]any{
			"ok": true, "endpoint": cli.Endpoint(), "elapsed_ms": elapsed,
			"groups": res.Groups, "hosts": len(res.Hosts),
			"failed_groups": res.FailedGroups, "partial": res.Partial,
		})
	}
}
