package handler

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/hostnet"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/tunnelprobe"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// 隧道自检相关的时间参数。
const (
	// platformProbeTimeout 平台侧异步拨测总超时。
	platformProbeTimeout = 8 * time.Second
	// agentCheckWindow 主机侧自检的有效窗口：超时未回报视为失败，
	// 避免主机离线时界面永远停在「待回报」。
	agentCheckWindow = 10 * time.Minute
)

// triggerTunnelCheck 保存隧道地址后自动做两件事（异步，不阻塞保存请求）：
//  1. 平台侧拨测一次（source=platform），立即给出地址级结论；
//  2. 标记待主机自检（source=agent），由下一次心跳下发给 Agent 从主机侧回连。
//
// 注意：这里不能用请求的 context（响应返回后会被取消），改用独立超时 context。
func triggerTunnelCheck(s *svc.ServiceContext, hostID int64) {
	// 先写入「待检查」状态，让列表立刻显示进度而不是旧结论。
	if _, err := s.DB.Exec(`
		UPDATE hosts
		SET tunnel_check_ok=NULL, tunnel_check_source='', tunnel_check_at=NULL,
		    tunnel_check_latency_ms=0, tunnel_check_detail='平台侧拨测中…',
		    tunnel_check_requested_at=NOW()
		WHERE id=$1`, hostID); err != nil {
		log.Printf("⚠️ 初始化隧道自检状态失败 host=%d: %v", hostID, err)
		return
	}
	go runPlatformTunnelCheck(s, hostID)
}

// runPlatformTunnelCheck 平台侧拨测并把结果写回。
func runPlatformTunnelCheck(s *svc.ServiceContext, hostID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), platformProbeTimeout*2)
	defer cancel()

	var serverURLOverride, agentDefault string
	err := s.DB.QueryRowContext(ctx, `
		SELECT COALESCE(server_url_override,''), COALESCE(agent_default_server_url,'')
		FROM hosts WHERE id=$1`, hostID).Scan(&serverURLOverride, &agentDefault)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("⚠️ 读取隧道地址失败 host=%d: %v", hostID, err)
		}
		return
	}
	target := strings.TrimSpace(serverURLOverride)
	if target == "" {
		// 覆盖被清空：清掉检查结果，列表不再标红。
		_, _ = s.DB.Exec(`UPDATE hosts
			SET tunnel_check_ok=NULL, tunnel_check_source='', tunnel_check_at=NULL,
			    tunnel_check_latency_ms=0, tunnel_check_detail='', tunnel_check_requested_at=NULL
			WHERE id=$1`, hostID)
		return
	}

	fallback := agentDefault
	if fallback == "" && s.Settings != nil {
		fallback = strings.TrimRight(strings.TrimSpace(s.Settings.Snapshot().Server.ExternalURL), "/")
	}
	resolved := hostnet.ResolveServerURL(target, fallback)
	if resolved == "" {
		savePlatformTunnelCheck(ctx, s.DB, hostID, tunnelprobe.Result{
			Target: target, OK: false, Hint: "连接地址非法：支持 IP、IP:端口或 http(s) URL",
		})
		return
	}

	res := tunnelprobe.Probe(ctx, resolved, platformProbeTimeout)
	savePlatformTunnelCheck(ctx, s.DB, hostID, res)
	if !res.OK {
		log.Printf("⚠️ 隧道拨测未通过 host=%d target=%s: %s", hostID, resolved, res.Hint)
	}
}

// savePlatformTunnelCheck 写入平台侧结果。
//
// 若主机侧自检已回报（source=agent），保留主机侧结论——它比平台侧更有说服力。
func savePlatformTunnelCheck(ctx context.Context, db *sql.DB, hostID int64, res tunnelprobe.Result) {
	if _, err := db.ExecContext(ctx, `
		UPDATE hosts
		SET tunnel_check_ok=$2, tunnel_check_source='platform',
		    tunnel_check_at=NOW(), tunnel_check_latency_ms=$3, tunnel_check_detail=$4
		WHERE id=$1 AND COALESCE(tunnel_check_source,'') <> 'agent'`,
		hostID, res.OK, res.LatencyMS, tunnelCheckDetail(res)); err != nil {
		log.Printf("⚠️ 保存隧道拨测结果失败 host=%d: %v", hostID, err)
	}
}

// saveAgentTunnelCheck 写入主机侧自检结果，并清除待检查标记。
func saveAgentTunnelCheck(ctx context.Context, db *sql.DB, agentID string, res tunnelprobe.Result) {
	if _, err := db.ExecContext(ctx, `
		UPDATE hosts
		SET tunnel_check_ok=$2, tunnel_check_source='agent',
		    tunnel_check_at=NOW(), tunnel_check_latency_ms=$3, tunnel_check_detail=$4,
		    tunnel_check_requested_at=NULL
		WHERE agent_id=$1`,
		agentID, res.OK, res.LatencyMS, tunnelCheckDetail(res)); err != nil {
		log.Printf("⚠️ 保存主机侧自检结果失败 agent=%s: %v", agentID, err)
	}
}

// tunnelCheckDetail 把拨测结果压缩成一行摘要（列表悬停/详情展示）。
func tunnelCheckDetail(res tunnelprobe.Result) string {
	parts := make([]string, 0, len(res.Stages)+1)
	for _, st := range res.Stages {
		mark := "✓"
		if !st.OK {
			mark = "✗"
		}
		parts = append(parts, fmt.Sprintf("%s %s（%dms）", mark, st.Name, st.LatencyMS))
	}
	if res.Hint != "" {
		parts = append(parts, res.Hint)
	}
	detail := strings.Join(parts, "；")
	const maxLen = 2000
	if len(detail) > maxLen {
		// 按字节截断可能切坏中文字符，PostgreSQL 的 UTF-8 校验会拒绝整个
		// UPDATE（表现为拨测结果永远写不进去）。这里丢弃不完整的尾字符。
		detail = strings.ToValidUTF8(detail[:maxLen], "") + "…"
	}
	return detail
}
