// Agent 网关 handlers：注册、心跳、长轮询取件、ack、stage、report。
package handler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/middleware"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/hostnet"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/netutil"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/safepath"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/tunnelprobe"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// AgentRegister Agent 注册（换取 token + secret）。
func AgentRegister(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req protocol.RegisterRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		arch := strings.ToLower(strings.TrimSpace(req.Arch))
		// 架构白名单：amd64/arm64/arm/386，可带 -legacy 后缀
		if !isValidArch(arch) {
			httpx.Fail(w, http.StatusBadRequest, "不支持的架构")
			return
		}
		// 安全：绝不信任请求体中的 req.IP。
		// 否则攻击者可传入他人 IP，借 hosts 表 ON CONFLICT(ip) DO UPDATE
		// 改写该主机的 agent_id，从而劫持并接收本应发往该主机的升级指令。
		ip := clientIP(s, r)

		snap := s.ConfigSnapshot()
		hostname := cleanAgentText(req.Hostname, 255)
		osName := strings.ToLower(cleanAgentText(req.OS, 64))
		osVersion := cleanAgentText(req.OSVersion, 64)
		kernel := cleanAgentText(req.Kernel, 128)
		supervisor := cleanAgentText(req.Supervisor, 32)
		agentVersion := cleanAgentText(req.AgentVersion, 64)

		// 未上报签名能力的旧 Agent 默认 id，保持“服务端升级不要求重装 Agent”。
		signatureModes := protocol.NormalizeSignatureModes(req.SignatureModes)
		if len(signatureModes) == 0 {
			signatureModes = []string{protocol.SignatureModeID}
		}
		capabilities := protocol.NormalizeCapabilities(req.Capabilities)
		buildVersion, buildHash, buildVariant := normalizeAgentBuild(req.BuildVersion, req.BuildHash, req.BuildVariant)
		buildVersion = cleanAgentText(buildVersion, 128)

		agentID := randHex(16)
		token := randHex(32)
		secret := randHex(32)

		// 注册的多次写入必须原子化：
		//   - 一次性令牌消费、主机绑定、Agent 创建要么全部成功、要么全部回滚；
		//   - 避免失败时留下孤儿 Agent 或白白消耗令牌；
		//   - hosts.ip 全局唯一，跨租户注册必须被拒绝，防止 NAT 后的同 IP
		//     用其他租户令牌劫持本租户主机的 agent_id。
		ctx := r.Context()
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			auditAnon(s, r, audit.ActionAgentRegister, hostname, "agent", ip, err)
			httpx.Fail(w, http.StatusInternalServerError, "注册失败")
			return
		}
		defer tx.Rollback()

		tenantID := int64(1)
		// 同一 IP 的注册串行化：避免并发重复注册为同一主机写入多个 Agent。
		// 事务级 advisory lock 在 commit/rollback 时自动释放。
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, ip); err != nil {
			auditAnon(s, r, audit.ActionAgentRegister, hostname, "agent", ip, err)
			httpx.Fail(w, http.StatusInternalServerError, "注册失败")
			return
		}
		// 校验并消费一次性 enroll token（默认要求；内网可信可关闭）。
		if snap.Agent.RequireEnrollToken {
			tid, ok := consumeEnrollTokenTx(ctx, tx, req.EnrollToken, ip)
			if !ok {
				auditAnon(s, r, audit.ActionAgentRegister, hostname, "agent", ip,
					fmt.Errorf("无效或已过期的注册令牌"))
				httpx.Fail(w, http.StatusUnauthorized, "无效或已过期的注册令牌")
				return
			}
			tenantID = tid
		}

		// 出口 IP 由 Agent 按路由探测上报；多网卡主机据此展示真实出口地址。
		// 只做格式校验，不参与主机身份（身份仍是服务端观察到的来源 IP）。
		exitIP := hostnet.NormalizeExitIP(req.ExitIP)

		// upsert 主机拿到 host_id；冲突且租户不同则不返回行。
		var hostID int64
		err = tx.QueryRowContext(ctx, `INSERT INTO hosts (ip, hostname, os_family, os_version, arch, kernel, agent_id, agent_version,
				agent_build_version, agent_build_hash, agent_build_variant, exit_ip, agent_status, supervisor, source, last_seen, tenant_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'online',$13,'agent',NOW(),$14)
			ON CONFLICT (ip) DO UPDATE SET hostname=EXCLUDED.hostname, os_family=COALESCE(NULLIF(EXCLUDED.os_family,''),hosts.os_family),
				arch=COALESCE(NULLIF(EXCLUDED.arch,''),hosts.arch), agent_id=EXCLUDED.agent_id,
				agent_version=EXCLUDED.agent_version,
				agent_build_version=EXCLUDED.agent_build_version,
				agent_build_hash=EXCLUDED.agent_build_hash,
				agent_build_variant=EXCLUDED.agent_build_variant,
				exit_ip=CASE WHEN EXCLUDED.exit_ip<>'' THEN EXCLUDED.exit_ip ELSE hosts.exit_ip END,
				agent_status='online', supervisor=EXCLUDED.supervisor,
				source='agent', last_seen=NOW()
			WHERE hosts.tenant_id = EXCLUDED.tenant_id
			RETURNING id`,
			ip, hostname, osName, osVersion, arch, kernel, agentID, agentVersion,
			buildVersion, buildHash, buildVariant, exitIP, supervisor, tenantID).Scan(&hostID)
		if err == sql.ErrNoRows {
			auditAnon(s, r, audit.ActionAgentRegister, hostname, "agent", ip,
				fmt.Errorf("IP 已属于其他租户"))
			httpx.Fail(w, http.StatusConflict, "该 IP 已属于其他租户，拒绝注册")
			return
		}
		if err != nil {
			auditAnon(s, r, audit.ActionAgentRegister, hostname, "agent", ip, err)
			httpx.Fail(w, http.StatusInternalServerError, "注册失败")
			return
		}

		// 注册即视为重新接入：撤销同 IP / 同主机上的旧 Agent 凭据，避免重装/换机后
		// 旧 token 仍然有效。放在主机 upsert 之后，保持 hosts -> agents 的
		// 加锁顺序（与 HostDelete/心跳一致），避免并发时死锁。
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agents WHERE ip=$1 OR host_id=$2`, ip, hostID); err != nil {
			auditAnon(s, r, audit.ActionAgentRegister, hostname, "agent", ip, err)
			httpx.Fail(w, http.StatusInternalServerError, "注册失败")
			return
		}

		// 安全：token 仅存哈希（SHA-256，token 为 256bit 随机，抗爆破）；明文只回给 Agent 一次。
		_, err = tx.ExecContext(ctx, `INSERT INTO agents (agent_id, host_id, ip, token_hash, secret, arch, os, agent_version, supervisor, signature_modes, capabilities, build_version, build_hash, build_variant, status, last_heartbeat_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'online',NOW())`,
			agentID, hostID, ip, agentgw.HashToken(token), secret, arch, osName, agentVersion, supervisor,
			strings.Join(signatureModes, ","), strings.Join(capabilities, ","), buildVersion, buildHash, buildVariant)
		if err != nil {
			auditAnon(s, r, audit.ActionAgentRegister, hostname, "agent", ip, err)
			httpx.Fail(w, http.StatusInternalServerError, "注册失败")
			return
		}
		if err := tx.Commit(); err != nil {
			auditAnon(s, r, audit.ActionAgentRegister, hostname, "agent", ip, err)
			httpx.Fail(w, http.StatusInternalServerError, "注册失败")
			return
		}

		auditAnon(s, r, audit.ActionAgentRegister, hostname, "agent", agentID, nil)
		maybeAutoUpdateAgent(s, r, agentID)
		httpx.OK(w, protocol.RegisterResponse{AgentID: agentID, Token: token, Secret: secret})
	}
}

// AgentHeartbeat 心跳（需认证，agentID 从认证上下文获取，防伪造）。
func AgentHeartbeat(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := middleware.GetAgentID(r)
		if agentID == "" {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		var req protocol.HeartbeatRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		status := normalizeAgentStatus(req.Status)
		// 签名能力与功能能力：只在本次上报非空时更新，旧 Agent 不覆盖数据库值。
		modes, caps := "", ""
		if len(req.SignatureModes) > 0 {
			if m := protocol.NormalizeSignatureModes(req.SignatureModes); len(m) > 0 {
				modes = strings.Join(m, ",")
			}
		}
		if len(req.Capabilities) > 0 {
			if c := protocol.NormalizeCapabilities(req.Capabilities); len(c) > 0 {
				caps = strings.Join(c, ",")
			}
		}
		buildVersion, buildHash, buildVariant := normalizeAgentBuild(req.BuildVersion, req.BuildHash, req.BuildVariant)
		// 维护窗口内不覆盖 upgrading 状态；RETURNING server_url_override 顺便取出
		// 该主机的连接地址覆盖，避免每次心跳多一次查询。
		//
		// 先更新 hosts、再更新 agents：与 Agent 注册/HostDelete 的
		// hosts -> agents 加锁顺序一致，避免与注册并发时形成死锁环。
		var (
			serverURLOverride string
			checkRequested    sql.NullTime
		)
		_ = s.DB.QueryRowContext(r.Context(), `UPDATE hosts SET last_seen=NOW(),
			agent_status=CASE WHEN maintenance_until IS NOT NULL AND maintenance_until>NOW() THEN agent_status ELSE $2 END,
			agent_build_version=CASE WHEN $4<>'' THEN $3 ELSE agent_build_version END,
			agent_build_hash=CASE WHEN $4<>'' THEN $4 ELSE agent_build_hash END,
			agent_build_variant=CASE WHEN $4<>'' THEN $5 ELSE agent_build_variant END,
			exit_ip=CASE WHEN $6<>'' THEN $6 ELSE exit_ip END,
			agent_server_url=CASE WHEN $7<>'' THEN $7 ELSE agent_server_url END,
			agent_default_server_url=CASE WHEN $8<>'' THEN $8 ELSE agent_default_server_url END
			WHERE agent_id=$1
			RETURNING COALESCE(server_url_override,''), tunnel_check_requested_at`, agentID, status, buildVersion, buildHash, buildVariant,
			hostnet.NormalizeExitIP(req.ExitIP),
			normalizeServerURL(req.AgentServerURL), normalizeServerURL(req.AgentDefaultServerURL)).
			Scan(&serverURLOverride, &checkRequested)
		// 旧 Agent 不上报 build_hash：不能用空值覆盖已有构建信息。
		// 新 Agent 上报 build_hash 后，即使 variant 为空也会写回（普通构建）。
		_, _ = s.DB.ExecContext(r.Context(), `UPDATE agents SET last_heartbeat_at=NOW(), status=$2,
			host_id=COALESCE(host_id,(SELECT id FROM hosts WHERE agent_id=$1 ORDER BY id LIMIT 1)),
			signature_modes=CASE WHEN $6<>'' THEN $6 ELSE signature_modes END,
			capabilities=CASE WHEN $7<>'' THEN $7 ELSE capabilities END,
			build_version=CASE WHEN $3<>'' THEN $3 ELSE build_version END,
			build_hash=CASE WHEN $4<>'' THEN $4 ELSE build_hash END,
			build_variant=CASE WHEN $4<>'' THEN $5 ELSE build_variant END
			WHERE agent_id=$1`, agentID, status, buildVersion, buildHash, buildVariant, modes, caps)
		// 自升级命令在 Agent 退出前无法上报 success，由新版本心跳按目标 SHA256 确认。
		if buildHash != "" {
			markSelfUpdateSuccess(r.Context(), s.DB, agentID, buildHash)
		}
		maybeAutoUpdateAgent(s, r, agentID)
		// 主机侧回连自检：Agent 在上一轮被要求拨测后，本轮心跳带回结果。
		if req.TunnelCheck != nil && req.TunnelCheck.URL != "" {
			saveAgentTunnelCheck(r.Context(), s.DB, agentID, protocolTunnelCheck(*req.TunnelCheck))
		}
		// 隧道地址：管理员为该主机配置过则在响应中下发，Agent 自动切换。
		// 每次心跳下发 clock_skew：设置页修改后，Agent 最迟一个心跳周期内生效。
		snap := s.Settings.Snapshot()
		resp := protocol.HeartbeatResponse{
			ServerURL: resolveServerURLOverride(s, r, serverURLOverride,
				normalizeServerURL(req.AgentDefaultServerURL)),
			ClockSkew: strings.TrimSpace(snap.Agent.ClockSkew),
		}
		if checkRequested.Valid {
			if time.Since(checkRequested.Time) < agentCheckWindow && resp.ServerURL != "" {
				// 仍在校验窗口内：继续下发待测地址，直到 Agent 回报结果。
				resp.TunnelCheckURL = resp.ServerURL
			} else {
				// 超时未回报（主机离线/旧 Agent 不支持）：清除标记，保留平台侧结论。
				_, _ = s.DB.ExecContext(r.Context(),
					`UPDATE hosts SET tunnel_check_requested_at=NULL WHERE agent_id=$1`, agentID)
			}
		}
		httpx.OK(w, resp)
	}
}

// resolveServerURLOverride 返回该主机应切换到的连接地址（未配置返回空串）。
//
// 该地址只是 Agent 的连接入口覆盖，与隧道建立无关。允许只填 IP 或 IP:端口，
// 此时按 Agent 上报的默认地址补齐协议与端口；Agent 未上报默认地址时
// 退化为设置页的 external_url。
func resolveServerURLOverride(s *svc.ServiceContext, r *http.Request, override, agentDefault string) string {
	raw := strings.TrimSpace(override)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		return hostnet.ResolveServerURL(raw, "")
	}
	fallback := agentDefault
	if fallback == "" && s.Settings != nil {
		snap := s.Settings.Snapshot()
		fallback = strings.TrimRight(strings.TrimSpace(snap.Server.ExternalURL), "/")
	}
	return hostnet.ResolveServerURL(raw, fallback)
}

// protocolTunnelCheck 把 Agent 上报的自检结果转成拨测结果（用于统一落库）。
func protocolTunnelCheck(r protocol.TunnelCheckResult) tunnelprobe.Result {
	res := tunnelprobe.Result{Target: r.URL, OK: r.OK, LatencyMS: r.LatencyMS}
	if !r.OK {
		res.Hint = r.Detail
	}
	return res
}

// normalizeServerURL 校验 Agent 上报的服务端地址（只允许 http/https 绝对地址）。
func normalizeServerURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.TrimRight(raw, "/")
}

// AgentPoll 长轮询取指令（需认证，原子取件；无指令则挂起至 poll_hold 秒或 ctx 取消）。
func AgentPoll(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := middleware.GetAgentID(r)
		if agentID == "" {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		var req protocol.PollRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		hold := s.ConfigSnapshot().Agent.PollHold
		if hold <= 0 {
			hold = 20
		}
		deadline := time.Now().Add(time.Duration(hold) * time.Second)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			cmd, err := s.AgentGW.Dequeue(r.Context(), agentID)
			if err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			if cmd != nil {
				httpx.OK(w, protocol.PollResponse{HasCommand: true, Command: cmd})
				return
			}
			if time.Now().After(deadline) {
				httpx.OK(w, protocol.PollResponse{HasCommand: false})
				return
			}
			select {
			case <-r.Context().Done():
				httpx.OK(w, protocol.PollResponse{HasCommand: false})
				return
			case <-ticker.C:
			}
		}
	}
}

// AgentCommandAck 指令确认（需认证）。
func AgentCommandAck(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := middleware.GetAgentID(r)
		if agentID == "" {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		var req protocol.AckRequest
		_ = httpx.DecodeJSON(r, &req)
		cmdID := r.PathValue("id")
		if err := s.AgentGW.Ack(r.Context(), agentID, cmdID, req.Status, req.Reason); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, nil)
	}
}

// AgentCommandStage 阶段上报（需认证，enter_maintenance 申请维护租约）。
func AgentCommandStage(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := middleware.GetAgentID(r)
		if agentID == "" {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		var req protocol.StageRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		cmdID := r.PathValue("id")
		if req.Stage == protocol.StageMaintenance {
			if err := s.AgentGW.EnterMaintenance(r.Context(), agentID, req.ExpectedSeconds, req.WillRestart); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		// 阶段日志：进入维护窗口也记录，任务详情页能看到完整执行链路。
		if err := s.AgentGW.UpdateStage(r.Context(), agentID, cmdID, req.Stage, req.Message); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, nil)
	}
}

// AgentCommandReport 结果/进度上报（需认证）。
func AgentCommandReport(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := middleware.GetAgentID(r)
		if agentID == "" {
			httpx.Fail(w, http.StatusUnauthorized, "未认证")
			return
		}
		var req protocol.ReportRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		cmdID := r.PathValue("id")
		if err := s.AgentGW.Report(r.Context(), agentID, cmdID, req); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, nil)
	}
}

// AgentEnrollToken 生成一次性注册令牌（管理员专用，需认证）。
func AgentEnrollToken(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := randHex(32)
		arch := r.URL.Query().Get("arch")
		if arch == "" {
			arch = "amd64"
		}
		osName := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("os")))
		if osName == "" {
			osName = "linux"
		}
		arch = strings.TrimSpace(strings.ToLower(arch))
		// 校验 OS + 架构组合（防注入与无效组合）
		if !isValidTarget(osName, arch) {
			httpx.Fail(w, http.StatusBadRequest, "不支持的操作系统或架构")
			return
		}
		// 持久化：存哈希，15 分钟有效期
		hash := agentgw.HashToken(token)
		snap := s.ConfigSnapshot()
		// TTL 取配置（默认 15m），不再硬编码。
		ttl := 15 * time.Minute
		if d, e := time.ParseDuration(snap.Agent.EnrollTokenTTL); e == nil && d > 0 {
			ttl = d
		}
		expiresAt := time.Now().Add(ttl)
		// 创建者/租户取自认证上下文（此路由已被 RequireAdmin 保护），不再硬编码 "admin"。
		creator := ""
		tenantID := int64(1)
		if u, ok := auth.FromContext(r.Context()); ok && u != nil {
			creator = u.Username
			if u.TenantID > 0 {
				tenantID = u.TenantID
			}
			// superadmin 可为指定租户生成令牌；普通管理员只能用自己租户。
			if u.IsSuperAdmin() {
				if v := strings.TrimSpace(r.URL.Query().Get("tenant_id")); v != "" {
					if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
						tenantID = n
					}
				}
			}
		}
		_, err := s.DB.ExecContext(r.Context(), `INSERT INTO enroll_tokens (token_hash, expires_at, created_by, tenant_id) VALUES ($1, $2, $3, $4)`,
			hash, expiresAt, creator, tenantID)
		if err != nil {
			auditOp(s, r, audit.ActionEnrollToken, "enroll_token", "", "", err)
			httpx.Fail(w, http.StatusInternalServerError, "生成令牌失败")
			return
		}
		auditOp(s, r, audit.ActionEnrollToken, "enroll_token", "", "", nil)

		// 使用设置页热更新后的快照，避免直接读共享 Config 产生数据竞争。
		serverURL := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
		// 校验 serverURL（防注入）。Linux 命令走 shell，需要额外转义单引号；
		// Windows 命令用 PowerShell 单引号字符串，转义规则不同，故取原始值。
		rawURL, err := validateURL(serverURL)
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "服务器 URL 配置错误")
			return
		}
		safeURL := shellEscape(rawURL)
		force := r.URL.Query().Get("force") == "1"
		installCmd := buildInstallCommand(safeURL, arch, token, force)
		if osName == "windows" {
			installCmd = buildWindowsInstallCommand(rawURL, arch, token, force)
		}
		httpx.OK(w, map[string]any{
			"enroll_token":    token,
			"expires_in":      int(ttl.Seconds()),
			"os":              osName,
			"arch":            arch,
			"install_command": installCmd,
			"base_url":        serverURL,
		})
	}
}

// installBaseURL 决定安装命令里的服务端地址。
//
// 优先使用设置页/配置文件里的 server.external_url；留空时回退到当前请求
// 的 Host（反向代理场景按 trust_proxy 决定是否采信 X-Forwarded-Proto）。
func installBaseURL(externalURL string, r *http.Request, trustProxy bool) string {
	if v := strings.TrimRight(strings.TrimSpace(externalURL), "/"); v != "" {
		return v
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if trustProxy {
		if p := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))); p == "http" || p == "https" {
			scheme = p
		}
	}
	return scheme + "://" + r.Host
}

// buildInstallCommand 生成一键安装命令。
//
// 必须先删除 /tmp/upgrade-agent：wget -c 会把上一次下载的旧二进制当成
// 完整文件跳过下载，导致重装后仍运行旧版本（例如仍带 401 缺陷的 Agent）。
func buildInstallCommand(safeURL, arch, token string, force bool) string {
	cmd := "rm -f /tmp/upgrade-agent \\\n" +
		"  && wget -c --tries=5 --timeout=30 --waitretry=5 -O /tmp/upgrade-agent \\\n" +
		"  '" + safeURL + "/api/agent/binary/linux-" + arch + "' \\\n" +
		"  && chmod +x /tmp/upgrade-agent \\\n" +
		"  && /tmp/upgrade-agent install --server '" + safeURL + "' --enroll-token '" + token + "'"
	if force {
		// 显式覆盖目标机已有 AgentID/Token/Secret，重新注册为当前服务端的新 Agent。
		cmd += " --force"
	}
	return cmd
}

// buildWindowsInstallCommand 生成 Windows 一键安装命令（PowerShell）。
//
// 兼容要求：
//   - Windows Server 2008 R2 自带 PowerShell 2.0：不能用 Invoke-WebRequest
//     （PS3+）与 DownloadFile 之外的新 API，故使用 System.Net.WebClient；
//   - 单引号字符串内的字面单引号需写成两个单引号（PowerShell 转义规则）；
//   - 先删除上次残留的 exe，避免重复接入时执行旧版本（与 Linux 命令同理）；
//   - 需在「管理员 PowerShell」中执行：注册 SCM 服务、写 Program Files 需要提权。
//   - cmd 用户可先执行 powershell -NoProfile -ExecutionPolicy Bypass 进入 PS。
func buildWindowsInstallCommand(safeURL, arch, token string, force bool) string {
	binURL := safeURL + "/api/agent/binary/windows-" + arch
	args := []string{"'install'", "'--server'", psQuote(safeURL), "'--enroll-token'", psQuote(token)}
	if force {
		args = append(args, "'--force'")
	}
	// 注意：psQuote 本身已包含两侧单引号，外层不能再加引号，
	// 否则会生成 ''http://...''，PowerShell 会报「方法调用中缺少“)”」。
	//
	// 用变量 $exe 保存临时路径，避免长表达式在控制台换行时被截断；
	// 每条语句独立成行，逐行粘贴也能执行。
	//
	// 下载失败时（目标机到平台不通）必须给出明确提示并停止，
	// 否则 Start-Process 只会报「系统找不到指定的文件」，掩盖真正的网络问题。
	// 用单行 try/catch 与 if/else：逐行粘贴时不会进入多行输入模式。
	return "$ErrorActionPreference = 'Stop'\n" +
		"$exe = Join-Path $env:TEMP 'upgrade-agent.exe'\n" +
		"Remove-Item -Force -ErrorAction SilentlyContinue $exe\n" +
		"try { (New-Object System.Net.WebClient).DownloadFile(" + psQuote(binURL) + ", $exe) } " +
		"catch { Write-Host ('下载失败: ' + $_.Exception.Message) -ForegroundColor Red; " +
		"Write-Host " + psQuote("请确认本机能访问 "+safeURL+"，可用 Test-NetConnection "+hostOnly(safeURL)+" -Port "+portOf(safeURL)+" 检查") + " -ForegroundColor Yellow }\n" +
		"if (Test-Path $exe) { Start-Process -FilePath $exe -ArgumentList " +
		strings.Join(args, ",") + " -Wait -NoNewWindow } " +
		"else { Write-Host '未下载到安装包，请先解决本机到平台的网络连通性' -ForegroundColor Red }"
}

// hostOnly 从 URL 中取出主机名（用于生成 Test-NetConnection 提示）。
func hostOnly(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "<服务器IP>"
	}
	return u.Hostname()
}

// portOf 从 URL 中取出端口，缺省按协议返回 80/443。
func portOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "8090"
	}
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// psQuote 把值包成 PowerShell 单引号字符串（内部 ' 写成 ”）。
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// splitBinaryName 拆分 {os}-{arch}[-legacy] 形式的下载名。
func splitBinaryName(name string) (osName, arch string, ok bool) {
	for _, cand := range []string{"windows", "linux"} {
		if strings.HasPrefix(name, cand+"-") {
			return cand, strings.TrimPrefix(name, cand+"-"), true
		}
	}
	return "", "", false
}

// consumeEnrollTokenTx 在校验事务内原子消费 enroll token，返回令牌所属租户。
//
// 使用 UPDATE ... RETURNING 而不是「先 SELECT 再 UPDATE」：并发注册时只有一个
// 事务能把 used_at 从 NULL 改为非空，另一个事务会在行锁释放后重新求值 WHERE，
// 得到 0 行并失败。令牌消费与后续注册写入在同一事务中，失败会一并回滚。
func consumeEnrollTokenTx(ctx context.Context, tx *sql.Tx, token, clientIP string) (int64, bool) {
	if token == "" || tx == nil {
		return 0, false
	}
	var tenantID int64
	err := tx.QueryRowContext(ctx, `
		UPDATE enroll_tokens
		SET used_at=NOW(), used_by_ip=$2
		WHERE token_hash=$1 AND used_at IS NULL AND expires_at > NOW()
		RETURNING COALESCE(tenant_id, 1)`,
		agentgw.HashToken(token), clientIP).Scan(&tenantID)
	if err != nil || tenantID <= 0 {
		return 0, false
	}
	return tenantID, true
}

// cleanAgentText 清洗 Agent 上报的短文本：去首尾空白、移除控制字符、
// 修正非法 UTF-8，并按数据库列宽按字节截断（不会切坏多字节字符）。
func cleanAgentText(s string, maxBytes int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	s = strings.TrimSpace(b.String())
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// normalizeAgentStatus 将 Agent 上报状态限制在数据库/页面认识的集合内。
//
// 不能直接落库任意字符串：agents.status 列宽有限，脏状态会让心跳失败，
// 也会让主机列表显示未知状态。未知值按 online 处理（心跳本身即在线证据）。
func normalizeAgentStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "online", "upgrading", "restarting", "crash_loop", "offline", "unknown":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "online"
	}
}

// clientIP 返回请求来源 IP。转发头是否可信由 server.trust_proxy 决定，
// 默认不信任（直接暴露时 XFF 可被任意伪造）。
func clientIP(s *svc.ServiceContext, r *http.Request) string {
	return netutil.ClientIP(r, s.ConfigSnapshot().Server.TrustProxy)
}

// isValidArch 校验架构白名单（防注入恶意文件名）。
func isValidArch(arch string) bool {
	switch arch {
	case "amd64", "arm64", "arm", "386",
		"amd64-legacy", "arm64-legacy", "arm-legacy", "386-legacy":
		return true
	}
	return false
}

// isValidTarget 校验 OS + 架构组合。
//
// Windows 仅支持 amd64/arm64，且没有 legacy 变体
// （Go 1.20 单套构建同时兼容 Server 2008 R2 ~ Windows 11）。
func isValidTarget(osName, arch string) bool {
	switch strings.TrimSpace(strings.ToLower(osName)) {
	case "", "linux":
		return isValidArch(arch)
	case "windows":
		return arch == "amd64" || arch == "arm64"
	default:
		return false
	}
}

// validateURL 校验 URL 合法性（http/https 绝对地址）。
func validateURL(rawURL string) (string, error) {
	if strings.ContainsAny(rawURL, "\r\n") {
		return "", fmt.Errorf("地址包含非法字符")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("不支持的协议: %s", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("缺少主机名")
	}
	return rawURL, nil
}

// shellEscape 按 POSIX 单引号规则转义（把 ' 替换为 '\”）。
func shellEscape(rawURL string) string {
	return strings.ReplaceAll(rawURL, "'", "'\\''")
}

// AgentBinaryDownload 提供 Agent 二进制下载（无鉴权，白名单文件）。
func AgentBinaryDownload(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		// 文件名白名单：{linux|windows}-{arch}[{-legacy}]
		osName, arch, ok := splitBinaryName(name)
		if !ok || !isValidTarget(osName, arch) {
			http.NotFound(w, r)
			return
		}
		dir := s.ConfigSnapshot().Server.AgentBinaryDir
		if dir == "" {
			dir = "./dist"
		}
		// 实际文件名：upgrade-agent-{name}[.exe]
		filename := "upgrade-agent-" + name
		if osName == "windows" {
			filename += ".exe"
		}
		fullPath := filepath.Join(dir, filename)
		// 防路径穿越：确保解析后仍在 dir 内
		if err := safepath.EnsureWithin(dir, fullPath); err != nil {
			http.NotFound(w, r)
			return
		}
		// 文件存在性检查
		info, err := os.Stat(fullPath)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		// http.ServeContent 支持 Range、ETag、If-Modified-Since
		f, err := os.Open(fullPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename="+filename)
		http.ServeContent(w, r, filename, info.ModTime(), f)
	}
}
