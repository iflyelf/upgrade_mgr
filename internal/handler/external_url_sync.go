package handler

import (
	"context"
	"log"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// SyncExternalURLToAgents 把 server.external_url 自动同步到已安装 Agent。
//
// 只更新两类主机：
//   - 尚未设置「连接地址覆盖」的主机；
//   - 上次由 external_url 自动写入（server_url_auto=true）的主机。
//
// 手工设置过连接地址覆盖的主机（server_url_auto=false 且 override 非空）保持不变。
// 更新后标记主机侧自检：Agent 下一次心跳切换地址，并在主机内部回连校验新地址。
// target 为空时清空自动同步的覆盖，让 Agent 回退到安装时的默认地址。
func SyncExternalURLToAgents(ctx context.Context, s *svc.ServiceContext, target string) (int64, error) {
	if s == nil || s.DB == nil {
		return 0, nil
	}
	target = strings.TrimRight(strings.TrimSpace(target), "/")
	if target == "" {
		res, err := s.DB.ExecContext(ctx, `
			UPDATE hosts
			SET server_url_override = '',
			    server_url_auto = FALSE,
			    tunnel_check_ok = NULL,
			    tunnel_check_source = '',
			    tunnel_check_at = NULL,
			    tunnel_check_latency_ms = 0,
			    tunnel_check_detail = '',
			    tunnel_check_requested_at = NULL
			WHERE server_url_auto = TRUE`)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return n, nil
	}

	res, err := s.DB.ExecContext(ctx, `
		UPDATE hosts
		SET server_url_override = $1,
		    server_url_auto = TRUE,
		    tunnel_check_ok = NULL,
		    tunnel_check_source = '',
		    tunnel_check_at = NULL,
		    tunnel_check_latency_ms = 0,
		    tunnel_check_detail = '等待主机侧自检…',
		    tunnel_check_requested_at = NOW()
		WHERE COALESCE(agent_id,'') <> ''
		  AND (server_url_auto = TRUE OR COALESCE(server_url_override,'') = '')`, target)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// maybeSyncExternalURLToAgents 在设置保存后按需触发自动同步。
//
// 只有开启 auto_sync_agent_url 且 external_url 发生变化（或刚开启该开关）时才执行，
// 避免页面每次保存全部设置都重复下发。
func maybeSyncExternalURLToAgents(ctx context.Context, s *svc.ServiceContext, beforeAuto, afterAuto bool, oldURL, newURL string) {
	if s == nil || s.Settings == nil {
		return
	}
	if !afterAuto {
		return
	}
	if oldURL == newURL && beforeAuto {
		return
	}
	n, err := SyncExternalURLToAgents(ctx, s, newURL)
	if err != nil {
		log.Printf("⚠️ 自动同步 external_url 到 Agent 失败: %v", err)
		return
	}
	if n > 0 {
		log.Printf("🔗 已将 external_url=%s 同步到 %d 台主机的连接地址覆盖，等待下一次心跳切换并自检", newURL, n)
	}
}
