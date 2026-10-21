// Agent 网关 handlers：注册、心跳、长轮询取件、ack、stage、report。
package handler

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/middleware"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/netutil"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/safepath"
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
		// 架构白名单：amd64/arm64/arm/386，可带 -legacy 后缀
		if !isValidArch(req.Arch) {
			httpx.Fail(w, http.StatusBadRequest, "不支持的架构")
			return
		}
		// 安全：绝不信任请求体中的 req.IP。
		// 否则攻击者可传入他人 IP，借 hosts 表 ON CONFLICT(ip) DO UPDATE
		// 改写该主机的 agent_id，从而劫持并接收本应发往该主机的升级指令。
		ip := clientIP(s, r)
		// 校验并消费一次性 enroll token（默认要求；内网可信可关闭）。
		if s.Config.Agent.RequireEnrollToken {
			if req.EnrollToken == "" || !consumeEnrollToken(s, req.EnrollToken) {
				auditAnon(s, r, audit.ActionAgentRegister, req.Hostname, "agent", ip,
					fmt.Errorf("无效或已过期的注册令牌"))
				httpx.Fail(w, http.StatusUnauthorized, "无效或已过期的注册令牌")
				return
			}
		}
		agentID := randHex(16)
		token := randHex(32)
		secret := randHex(32)

		// 安全：token 仅存哈希（SHA-256，token 为 256bit 随机，抗爆破）；明文只回给 Agent 一次。
		_, err := s.DB.ExecContext(r.Context(), `INSERT INTO agents (agent_id, ip, token_hash, secret, arch, os, agent_version, supervisor, status, last_heartbeat_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'online',NOW())`,
			agentID, ip, agentgw.HashToken(token), secret, req.Arch, req.OS, req.AgentVersion, req.Supervisor)
		if err != nil {
			auditAnon(s, r, audit.ActionAgentRegister, req.Hostname, "agent", ip, err)
			httpx.Fail(w, http.StatusInternalServerError, "注册失败")
			return
		}
		_, err = s.DB.ExecContext(r.Context(), `INSERT INTO hosts (ip, hostname, os_family, os_version, arch, kernel, agent_id, agent_version, agent_status, supervisor, source, last_seen)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'online',$9,'agent',NOW())
			ON CONFLICT (ip) DO UPDATE SET hostname=EXCLUDED.hostname, os_family=COALESCE(NULLIF(EXCLUDED.os_family,''),hosts.os_family),
				arch=COALESCE(NULLIF(EXCLUDED.arch,''),hosts.arch), agent_id=EXCLUDED.agent_id,
				agent_version=EXCLUDED.agent_version, agent_status='online', supervisor=EXCLUDED.supervisor, last_seen=NOW()`,
			ip, req.Hostname, req.OS, req.OSVersion, req.Arch, req.Kernel, agentID, req.AgentVersion, req.Supervisor)
		if err != nil {
			auditAnon(s, r, audit.ActionAgentRegister, req.Hostname, "agent", ip, err)
			httpx.Fail(w, http.StatusInternalServerError, "注册失败")
			return
		}

		auditAnon(s, r, audit.ActionAgentRegister, req.Hostname, "agent", agentID, nil)
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
		status := req.Status
		if status == "" {
			status = "online"
		}
		_, _ = s.DB.Exec(`UPDATE agents SET last_heartbeat_at=NOW(), status=$2 WHERE agent_id=$1`, agentID, status)
		// 维护窗口内不覆盖 upgrading 状态
		_, _ = s.DB.Exec(`UPDATE hosts SET last_seen=NOW(),
			agent_status=CASE WHEN maintenance_until IS NOT NULL AND maintenance_until>NOW() THEN agent_status ELSE $2 END
			WHERE agent_id=$1`, agentID, status)
		httpx.OK(w, nil)
	}
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
		hold := s.Config.Agent.PollHold
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
		if err := s.AgentGW.Ack(r.Context(), agentID, cmdID, req.Status); err != nil {
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
		if req.Stage == protocol.StageMaintenance {
			if err := s.AgentGW.EnterMaintenance(r.Context(), agentID, req.ExpectedSeconds, req.WillRestart); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
		} else {
			_ = s.AgentGW.UpdateStage(r.Context(), agentID, req.Stage)
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
		// 校验架构参数
		if !isValidArch(arch) {
			httpx.Fail(w, http.StatusBadRequest, "不支持的架构")
			return
		}
		// 持久化：存哈希，15 分钟有效期
		hash := agentgw.HashToken(token)
		// TTL 取配置（默认 15m），不再硬编码。
		ttl := 15 * time.Minute
		if d, e := time.ParseDuration(s.Config.Agent.EnrollTokenTTL); e == nil && d > 0 {
			ttl = d
		}
		expiresAt := time.Now().Add(ttl)
		// 创建者取自认证上下文（此路由已被 RequireAdmin 保护），不再硬编码 "admin"。
		creator := ""
		if u, ok := auth.FromContext(r.Context()); ok && u != nil {
			creator = u.Username
		}
		_, err := s.DB.ExecContext(r.Context(), `INSERT INTO enroll_tokens (token_hash, expires_at, created_by) VALUES ($1, $2, $3)`,
			hash, expiresAt, creator)
		if err != nil {
			auditOp(s, r, audit.ActionEnrollToken, "enroll_token", "", "", err)
			httpx.Fail(w, http.StatusInternalServerError, "生成令牌失败")
			return
		}
		auditOp(s, r, audit.ActionEnrollToken, "enroll_token", "", "", nil)

		// 使用设置页热更新后的快照，避免直接读共享 Config 产生数据竞争。
		snap := s.Settings.Snapshot()
		serverURL := installBaseURL(snap.Server.ExternalURL, r, snap.Server.TrustProxy)
		// 校验与转义 serverURL（防注入）
		safeURL, err := validateAndEscapeURL(serverURL)
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "服务器 URL 配置错误")
			return
		}
		force := r.URL.Query().Get("force") == "1"
		installCmd := buildInstallCommand(safeURL, arch, token, force)
		httpx.OK(w, map[string]any{
			"enroll_token":    token,
			"expires_in":      int(ttl.Seconds()),
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

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// consumeEnrollToken 校验并一次性消费 enroll token（防滥用注册）。
// 内部做 SHA-256 哈希比对（存储的是哈希），找到且未过期未使用则标记已使用并返回 true。
func consumeEnrollToken(s *svc.ServiceContext, token string) bool {
	if token == "" {
		return false
	}
	hash := agentgw.HashToken(token)
	// 查询：未使用 + 未过期
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM enroll_tokens WHERE token_hash=$1 AND used_at IS NULL AND expires_at > NOW()`, hash).Scan(&id)
	if err != nil {
		return false
	}
	// CAS 原子标记已使用（防并发重复注册）
	res, err := s.DB.Exec(`UPDATE enroll_tokens SET used_at=NOW() WHERE id=$1 AND used_at IS NULL`, id)
	if err != nil {
		return false
	}
	rows, _ := res.RowsAffected()
	return rows == 1
}

// clientIP 返回请求来源 IP。转发头是否可信由 server.trust_proxy 决定，
// 默认不信任（直接暴露时 XFF 可被任意伪造）。
func clientIP(s *svc.ServiceContext, r *http.Request) string {
	return netutil.ClientIP(r, s.Config.Server.TrustProxy)
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

// validateAndEscapeURL 校验 URL 合法性（http/https）并转义单引号（防 shell 注入）。
func validateAndEscapeURL(rawURL string) (string, error) {
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
	// 转义单引号为 '\''（shell 单引号内的转义方式）
	return strings.ReplaceAll(rawURL, "'", "'\\''"), nil
}

// AgentBinaryDownload 提供 Agent 二进制下载（无鉴权，白名单文件）。
func AgentBinaryDownload(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		// 文件名白名单：linux-{arch}[{-legacy}]
		if !strings.HasPrefix(name, "linux-") {
			http.NotFound(w, r)
			return
		}
		arch := strings.TrimPrefix(name, "linux-")
		if !isValidArch(arch) {
			http.NotFound(w, r)
			return
		}
		dir := s.Config.Server.AgentBinaryDir
		if dir == "" {
			dir = "./dist"
		}
		// 实际文件名：upgrade-agent-{name}
		filename := "upgrade-agent-" + name
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
