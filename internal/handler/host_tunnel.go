package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"database/sql"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/hostnet"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/tunnelprobe"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// tunnelTestRequest 拨测参数。
type tunnelTestRequest struct {
	// Target 可选：测试尚未保存的候选地址（如刚在表单里填的值）。
	// 留空则测试该主机已保存的平台侧隧道地址。
	Target string `json:"target"`
	// TimeoutSeconds 单步超时，默认 5 秒，上限 30 秒。
	TimeoutSeconds int `json:"timeout_seconds"`
}

// HostTunnelTest 从平台侧拨测连接地址（管理员，租户隔离）。
//
// 说明：平台不反连主机，这里拨测的是「Agent 要连接的平台地址」，
// 用于在保存前发现地址填错、端口未放行、TLS 证书不匹配等问题。
// 它不参与隧道建立，也不能替代主机侧实测（隧道可能仅主机网段可达）。
func HostTunnelTest(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "主机 ID 无效")
			return
		}
		var req tunnelTestRequest
		// 允许空 body（直接测试已保存地址）。
		_ = httpx.DecodeJSON(r, &req)
		timeout := time.Duration(req.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = tunnelprobe.DefaultTimeout
		}
		if timeout > 30*time.Second {
			timeout = 30 * time.Second
		}

		var (
			ip, tunnelIP, serverURLOverride, agentDefault string
			opErr                                         error
		)
		defer func() {
			auditOpDetails(s, r, audit.ActionHostTunnelTest, "host", strconv.FormatInt(id, 10), ip, opErr,
				map[string]any{"tunnel_test": req.Target, "ok": opErr == nil}, nil)
		}()

		cond, condArgs := reqScope(r).Where("", 2)
		err = s.DB.QueryRowContext(r.Context(), `
			SELECT ip, COALESCE(tunnel_ip,''), COALESCE(server_url_override,''),
			       COALESCE(agent_default_server_url,'')
			FROM hosts WHERE id=$1 AND `+cond,
			append([]any{id}, condArgs...)...).
			Scan(&ip, &tunnelIP, &serverURLOverride, &agentDefault)
		if errors.Is(err, sql.ErrNoRows) {
			opErr = err
			httpx.Fail(w, http.StatusNotFound, "主机不存在或无权访问")
			return
		}
		if err != nil {
			opErr = err
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}

		target := strings.TrimSpace(req.Target)
		if target == "" {
			target = serverURLOverride
		}
		if target == "" {
			httpx.Fail(w, http.StatusBadRequest,
				"该主机未配置连接地址覆盖（也可在请求体中传 target 测试候选地址）")
			return
		}

		fallback := agentDefault
		if fallback == "" && s.Settings != nil {
			fallback = strings.TrimRight(strings.TrimSpace(s.Settings.Snapshot().Server.ExternalURL), "/")
		}
		resolved := hostnet.ResolveServerURL(target, fallback)
		if resolved == "" {
			httpx.Fail(w, http.StatusBadRequest, "连接地址非法：支持 IP、IP:端口或 http(s) URL")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout*4)
		defer cancel()
		result := tunnelprobe.Probe(ctx, resolved, timeout)
		if !result.OK {
			opErr = errors.New("隧道拨测未通过")
		}
		// 手动拨测同时更新主机上的自检状态，列表标红/转绿与手动结果一致。
		if _, err := s.DB.ExecContext(r.Context(), `
			UPDATE hosts
			SET tunnel_check_ok=$2, tunnel_check_source='platform',
			    tunnel_check_at=NOW(), tunnel_check_latency_ms=$3, tunnel_check_detail=$4
			WHERE id=$1`, id, result.OK, result.LatencyMS, tunnelCheckDetail(result)); err != nil {
			log.Printf("⚠️ 保存手动拨测结果失败 host=%d: %v", id, err)
		}
		httpx.OK(w, map[string]any{
			"host_id":   id,
			"ip":        ip,
			"tunnel_ip": tunnelIP,
			"target":    target,
			"result":    result,
		})
	}
}
