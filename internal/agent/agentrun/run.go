// Package agentrun Agent 主循环：注册→心跳→长轮询取指令→执行→上报。
package agentrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/agent/executor"
	"github.com/iflyelf/upgrade_mgr/internal/agent/osinfo"
	"github.com/iflyelf/upgrade_mgr/internal/agent/selfupdate"
	"github.com/iflyelf/upgrade_mgr/internal/agent/svcmgr"
	"github.com/iflyelf/upgrade_mgr/internal/agent/tlsclient"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/hostnet"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/netinfo"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/signer"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/tunnelprobe"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// Config Agent 运行配置
type Config struct {
	// ServerURL 是 Agent 当前实际连接的服务端地址：管理员为该主机配置
	// 隧道地址后会被心跳响应覆盖。
	ServerURL string
	// DefaultServerURL 是安装时写入的默认地址。连接地址被覆盖后保留原值，
	// 服务端清空覆盖时 Agent 据此回退，避免配置误删后失去连接。
	DefaultServerURL string `json:"default_server_url,omitempty"`
	// ServerURLs 是备用连接地址列表（安装时用逗号分隔多个 --server 或手工写入
	// state）。当前地址连续连接失败时按顺序轮换，减少对服务端下发覆盖的依赖。
	// 隧道由隧道软件负责建立；这里只负责"连哪个入口"。
	ServerURLs    []string `json:"server_urls,omitempty"`
	EnrollToken   string
	AgentID       string
	Token         string
	Secret        string
	StatePath     string
	LastHeartbeat time.Time `json:"last_heartbeat"` // 用于 watchdog 检测
	// Version/Variant 是编译期注入的构建信息，不写入 state：
	// 自升级替换二进制并重启后，新进程会自动使用新值。
	Version string `json:"-"`
	Variant string `json:"-"`
	// PendingReports 是尚未成功送达服务端的最终结果。
	// 网络抖动、服务端瞬时 5xx 或进程重启时，心跳会继续补报；
	// 服务端按 cmdID + agentID 幂等落库，因此重复上报是安全的。
	PendingReports map[string]protocol.ReportRequest `json:"pending_reports,omitempty"`
	// ExitIP 最近一次成功探测到的本机出口 IP，用于心跳上报。
	ExitIP string `json:"exit_ip,omitempty"`
	// ---- 可选 TLS / 双向 TLS ----
	// TLSCA 校验服务端证书的 CA（PEM 路径）；TLSCert/TLSKey 为客户端证书，
	// 服务端开启 tls_client_auth=require 时必须配置。
	TLSCA   string `json:"tls_ca,omitempty"`
	TLSCert string `json:"tls_cert,omitempty"`
	TLSKey  string `json:"tls_key,omitempty"`
	// AppliedOverride 当前生效的「连接地址覆盖」；用于 status 判断地址来源。
	// 服务端清空覆盖、覆盖地址失败回退时清空。
	AppliedOverride string `json:"applied_override,omitempty"`
	// ---- 最近一次连接地址切换（供 `upgrade-agent status` 现场排查）----
	// 持久化在 state.json，跨进程可见；不包含任何凭据。
	LastSwitchFrom   string    `json:"last_switch_from,omitempty"`
	LastSwitchReason string    `json:"last_switch_reason,omitempty"`
	LastSwitchAt     time.Time `json:"last_switch_at,omitempty"`
}

// Agent 主体
type Agent struct {
	cfg       Config
	info      osinfo.Info
	svcMgr    svcmgr.Manager
	client    *http.Client
	buildHash string // 当前可执行文件 SHA256，用于服务端判断是否需要自升级
	// executed 记录本进程已执行过的指令 ID，防止至少一次投递导致重复升级。
	// 只在验签通过、准备执行时写入；进程重启后服务端不会重新投递已派发指令。
	executed map[string]time.Time
	// connectFailures 连续多少次心跳失败。不落盘：进程重启后重新计数，
	// 避免旧值导致立即回退。在覆盖地址上达到阈值即退回默认地址，
	// 在默认地址上达到阈值则轮换到下一个备用地址。
	connectFailures int
	// tunnelFailURL/tunnelFailAt 记录刚失败的隧道地址，短期内不再重复切换，
	// 避免「切过去失败 → 回退 → 服务端再次下发 → 再失败」的来回抖动。
	tunnelFailURL string
	tunnelFailAt  time.Time
	// 主机侧回连自检：服务端在心跳响应中下发待测地址，Agent 拨测后把结果
	// 放在下一次心跳里回报（tunnelPending），避免阻塞心跳请求。
	tunnelMu      sync.Mutex
	tunnelPending *protocol.TunnelCheckResult
	tunnelProbing bool
	tunnelLastURL string
	tunnelLastAt  time.Time
}

// tunnelRetryAfter 隧道地址失败后的重试间隔。
//
// 隧道恢复后最迟 5 分钟自动重新尝试；这期间服务端即使继续下发同一地址，
// Agent 也保持默认地址，保证连接不中断。
const tunnelRetryAfter = 5 * time.Minute

const (
	maxPendingReports      = 20
	maxAgentReportLogBytes = 64 * 1024
)

// New 创建 Agent
func New(cfg Config) *Agent {
	// 容忍 --server 末尾多写斜杠，避免请求路径变成 //api/...
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	cfg.ServerURLs = normalizeServerList(cfg.ServerURL, cfg.ServerURLs)
	if cfg.DefaultServerURL == "" {
		// 旧 state 没有该字段：把当前地址当作默认地址，保证可回退。
		cfg.DefaultServerURL = cfg.ServerURL
	}
	cfg.DefaultServerURL = strings.TrimRight(cfg.DefaultServerURL, "/")
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	info := osinfo.Detect()
	if cfg.PendingReports == nil {
		cfg.PendingReports = map[string]protocol.ReportRequest{}
	}
	client, err := tlsclient.New(tlsclient.Config{
		CAFile: cfg.TLSCA, CertFile: cfg.TLSCert, KeyFile: cfg.TLSKey,
	})
	if err != nil {
		// TLS 配置错误不应静默降级为明文：记录后由首次请求失败暴露问题。
		log.Printf("⚠️ TLS 客户端配置失败（将无法建立 TLS 连接）: %v", err)
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Agent{
		cfg:       cfg,
		info:      info,
		svcMgr:    svcmgr.New(info.Init),
		client:    client,
		buildHash: executableSHA256(),
		executed:  map[string]time.Time{},
	}
}

// executableSHA256 计算当前 Agent 二进制的 SHA256。
// 服务端把它与待下发二进制的哈希比较，判断 Agent 是否已是最新版本。
func executableSHA256() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Run 运行主循环
func (a *Agent) Run(ctx context.Context) error {
	// 1. 注册（如果还没有凭据）
	if a.cfg.AgentID == "" {
		if a.cfg.EnrollToken == "" {
			return fmt.Errorf("缺少注册令牌：请用 install --enroll-token 安装，或删除 %s 后重新注册", a.cfg.StatePath)
		}
		if err := a.register(); err != nil {
			if strings.Contains(err.Error(), "HTTP 401") {
				return fmt.Errorf("注册失败: 注册令牌无效或已过期（HTTP 401），请在控制台重新生成令牌后再安装: %w", err)
			}
			if strings.Contains(err.Error(), "HTTP 429") {
				return fmt.Errorf("注册失败: 注册请求过于频繁（HTTP 429），请稍后重试: %w", err)
			}
			return fmt.Errorf("注册失败: %w", err)
		}
		// 注册令牌是一次性的，成功后不再落盘；state 只保留 Agent 凭据。
		a.cfg.EnrollToken = ""
		if err := a.saveState(); err != nil {
			log.Printf("⚠️ 保存 Agent 凭据失败（进程重启后需重新注册）: %v", err)
		}
		log.Printf("注册成功: agent_id=%s", a.cfg.AgentID)
	} else if a.cfg.Token == "" || a.cfg.Secret == "" {
		// 凭据残缺时继续运行只会在心跳/取指令阶段反复 401，
		// 直接给出可操作的错误，避免 systemd 无限重启。
		return fmt.Errorf("Agent 状态文件缺少 token/secret，请删除 %s 后重新执行安装命令", a.cfg.StatePath)
	}

	// 进程重启后先补报上次未送达的最终结果，避免任务永久停在执行中。
	a.flushPendingReports()

	// 2. 心跳 ticker
	heartbeatTicker := time.NewTicker(30 * time.Second)
	defer heartbeatTicker.Stop()

	// 3. 长轮询循环
	for {
		select {
		case <-ctx.Done():
			// 正常停止（服务停止/收到信号）返回 nil，避免 Windows SCM 判定为崩溃并触发恢复动作。
			return nil
		case <-heartbeatTicker.C:
			a.heartbeat()
		default:
			a.poll(ctx)
		}
	}
}

func (a *Agent) register() error {
	hostname, _ := os.Hostname()
	req := protocol.RegisterRequest{
		EnrollToken:    a.cfg.EnrollToken,
		Hostname:       hostname,
		IP:             a.getLocalIP(),
		OS:             a.info.OSFamily,
		OSVersion:      a.info.OSVersion,
		ExitIP:         a.exitIP(),
		Arch:           a.info.Arch,
		Kernel:         a.info.Kernel,
		Supervisor:     a.info.Init,
		AgentVersion:   "0.1.0",
		BuildVersion:   a.cfg.Version,
		BuildHash:      a.buildHash,
		BuildVariant:   a.cfg.Variant,
		Components:     map[string]string{},
		SignatureModes: protocol.SupportedSignatureModes(),
		Capabilities:   protocol.SupportedCapabilities(),
	}
	var resp protocol.RegisterResponse
	if err := a.postJSON("/api/agent/register", req, &resp); err != nil {
		return err
	}
	a.cfg.AgentID = resp.AgentID
	a.cfg.Token = resp.Token
	a.cfg.Secret = resp.Secret
	return nil
}

func (a *Agent) heartbeat() {
	a.tunnelMu.Lock()
	pending := a.tunnelPending
	a.tunnelMu.Unlock()

	req := protocol.HeartbeatRequest{
		AgentID:               a.cfg.AgentID,
		Busy:                  false,
		Status:                "online",
		ExitIP:                a.exitIP(),
		AgentServerURL:        a.cfg.ServerURL,
		AgentDefaultServerURL: a.cfg.DefaultServerURL,
		BuildVersion:          a.cfg.Version,
		BuildHash:             a.buildHash,
		BuildVariant:          a.cfg.Variant,
		SignatureModes:        protocol.SupportedSignatureModes(),
		Capabilities:          protocol.SupportedCapabilities(),
		TunnelCheck:           pending,
	}
	var resp protocol.HeartbeatResponse
	err := a.postJSON("/api/agent/heartbeat", req, &resp)
	if err != nil {
		log.Printf("⚠️ 心跳失败: %v", err)
		a.handleConnectFailure()
	} else {
		// 结果已送达：清除待上报值（仅当期间没有新结果覆盖）。
		if pending != nil {
			a.tunnelMu.Lock()
			if a.tunnelPending == pending {
				a.tunnelPending = nil
			}
			a.tunnelMu.Unlock()
		}
		// 服务端要求从主机侧回连自检。
		if resp.TunnelCheckURL != "" {
			a.startTunnelProbe(resp.TunnelCheckURL)
		}
	}
	if err == nil && a.applyServerURL(resp.ServerURL) {
		// 隧道地址变化（含被服务端清空后回退默认地址）：立即落盘，
		// 后续心跳/轮询/下载都使用新地址。
		a.cfg.LastHeartbeat = time.Now()
		if err := a.saveState(); err != nil {
			log.Printf("⚠️ 保存服务端地址失败: %v", err)
		}
		return
	}
	a.cfg.LastHeartbeat = time.Now()
	if err := a.saveState(); err != nil {
		log.Printf("⚠️ 保存心跳状态失败: %v", err)
	}
	// 心跳是稳定的 30s 周期，用它补报执行结果，避免一次网络抖动导致任务卡死。
	a.flushPendingReports()
}

func (a *Agent) poll(ctx context.Context) {
	req := protocol.PollRequest{AgentID: a.cfg.AgentID, Busy: false}
	var resp protocol.PollResponse
	if err := a.postJSON("/api/agent/commands/poll", req, &resp); err != nil {
		time.Sleep(5 * time.Second)
		return
	}
	if !resp.HasCommand || resp.Command == nil {
		return
	}
	cmd := *resp.Command
	log.Printf("收到指令: id=%s type=%s component=%s", cmd.ID, cmd.Type, cmd.Payload.Component.Name)

	// 验签 + 过期校验：拒绝伪造/重放/过期指令
	if err := a.verifyCommand(cmd); err != nil {
		log.Printf("指令验签失败，拒绝执行: %v", err)
		_ = a.postJSON("/api/agent/commands/"+cmd.ID+"/ack",
			protocol.AckRequest{AgentID: a.cfg.AgentID, Status: "rejected", Reason: err.Error()}, nil)
		return
	}

	// 至少一次投递 + 进程内去重：同一指令 ID 只执行一次。
	// 若上一次执行结果还没送达，心跳会负责补报。
	if a.wasExecuted(cmd.ID) {
		log.Printf("忽略重复指令: id=%s（本进程已执行过）", cmd.ID)
		return
	}

	// ACK
	_ = a.postJSON("/api/agent/commands/"+cmd.ID+"/ack",
		protocol.AckRequest{AgentID: a.cfg.AgentID, Status: "accepted"}, nil)
	a.markExecuted(cmd.ID)

	switch cmd.Type {
	case protocol.CmdUpgradeComponent, protocol.CmdRollbackComponent:
		a.executeUpgrade(ctx, cmd)
	case protocol.CmdRunCommand:
		a.executeRunCommand(ctx, cmd)
	case protocol.CmdSelfUpdate:
		a.executeSelfUpdate(ctx, cmd)
	default:
		log.Printf("暂不支持的指令类型: %s", cmd.Type)
	}
}

// executeRunCommand 执行一次性脚本/命令（kind=command 任务）。
//
// 不进入组件升级流水线，也不做备份/停服/回滚；复用阶段上报，
// 让任务详情页能实时看到执行输出。
func (a *Agent) executeRunCommand(ctx context.Context, cmd protocol.Command) {
	spec := protocol.CommandSpec{}
	if cmd.Payload.Command != nil {
		spec = *cmd.Payload.Command
	}
	a.postStage(cmd.ID, protocol.StageRequest{
		AgentID: a.cfg.AgentID, Stage: protocol.StageRunCommand,
		Message: "开始执行命令\n",
	})
	res := executor.ExecuteCommand(ctx, spec, func(chunk string) {
		// 实时输出：按块上报阶段消息，任务详情页无需等最终结果即可看到日志。
		// 服务端对 run_command 阶段按原文拼接（不加时间戳前缀），保留脚本原始排版。
		if chunk == "" {
			return
		}
		a.postStage(cmd.ID, protocol.StageRequest{
			AgentID: a.cfg.AgentID, Stage: protocol.StageRunCommand,
			Message: chunk,
		})
	}, func(stage, message string) {
		a.postStage(cmd.ID, protocol.StageRequest{
			AgentID: a.cfg.AgentID, Stage: stage, Message: message,
		})
	})
	if res == nil {
		res = &executor.CommandResult{ExitCode: -1}
	}
	// 先上报终态阶段：即使最终 report 丢失，服务端也能据 done/failed 阶段闭环，
	// 不会让命令任务永久停在执行中。
	if res.Success {
		a.postStage(cmd.ID, protocol.StageRequest{
			AgentID: a.cfg.AgentID, Stage: protocol.StageDone, Message: "命令执行完成",
		})
	} else {
		a.postStage(cmd.ID, protocol.StageRequest{
			AgentID: a.cfg.AgentID, Stage: protocol.StageFailed,
			Message: fmt.Sprintf("命令执行失败（exit %d）", res.ExitCode),
		})
	}
	report := protocol.ReportRequest{
		AgentID: a.cfg.AgentID, Stage: protocol.StageDone, ExitCode: res.ExitCode, Log: res.Log,
	}
	if res.Success {
		report.Status = protocol.CmdSuccess
	} else {
		report.Status = protocol.CmdFailed
		report.Stage = protocol.StageFailed
		if res.Log == "" {
			report.Log = "[exec] 命令执行失败\n"
		}
	}
	_ = a.postReportWithRetry(cmd.ID, report)
}

func (a *Agent) executeSelfUpdate(ctx context.Context, cmd protocol.Command) {
	// 自升级：下载→备份→替换→exit(75)，由 systemd/upstart 重启拉起新版本。
	// 服务端下发绝对 URL；这里兼容相对路径，且只对平台自身地址携带 Agent 凭据。
	artifactURL, downloadAuth := a.resolveArtifactURL(cmd.Payload.Artifact.URL)
	cmd.Payload.Artifact.URL = artifactURL

	log.Printf("执行自升级到版本 %s", cmd.Payload.ToVersion)
	a.postStage(cmd.ID, protocol.StageRequest{
		AgentID: a.cfg.AgentID, Stage: protocol.StageMaintenance,
		Message: "Agent 自升级，进入维护窗口", ExpectedSeconds: 600, WillRestart: true,
	})
	a.postStage(cmd.ID, protocol.StageRequest{
		AgentID: a.cfg.AgentID, Stage: protocol.StageDownloading,
		Message: "下载 Agent 二进制",
	})

	binPath, _ := os.Executable()
	if err := selfupdate.Execute(ctx, cmd, binPath, downloadAuth); err != nil {
		log.Printf("自升级失败: %v", err)
		a.postStage(cmd.ID, protocol.StageRequest{
			AgentID: a.cfg.AgentID, Stage: protocol.StageFailed,
			Message: fmt.Sprintf("自升级失败: %v", err),
		})
		report := protocol.ReportRequest{
			AgentID: a.cfg.AgentID, Status: protocol.CmdFailed, Stage: protocol.StageFailed,
			Log: fmt.Sprintf("[%s] 自升级失败: %v\n", time.Now().Format("2006-01-02 15:04:05"), err),
		}
		_ = a.postReportWithRetry(cmd.ID, report)
	}
	// 成功则 exit(75) 不会执行到这里
}

// verifyCommand 校验指令 HMAC 签名与过期时间（防伪造/重放）。
func (a *Agent) verifyCommand(cmd protocol.Command) error {
	if a.cfg.Secret == "" {
		return fmt.Errorf("缺少 agent 密钥")
	}
	if !cmd.ExpireAt.IsZero() && time.Now().After(cmd.ExpireAt) {
		return fmt.Errorf("指令已过期")
	}
	body, _ := json.Marshal(cmd.Payload)
	env := signer.Envelope{
		Method: "CMD", Body: body,
		Timestamp: cmd.Timestamp, Nonce: cmd.Nonce,
	}

	// 新服务端同时下发 signature_v2。存在时必须校验通过，
	// 不能回退到旧签名，否则攻击者可篡改 v2 签名实施降级。
	if cmd.SignatureV2 != "" {
		env.Path = cmd.SignedPath()
		return signer.Verify(a.cfg.Secret, cmd.SignatureV2, env, 10*time.Minute)
	}

	// 兼容旧服务端：signature_v2 缺失时，signature 可能是新版 SignedPath，
	// 也可能只签了 ID。先按新版校验，失败再按旧版校验；两种服务端均可工作。
	// 注意：缺失时的回退是为了兼容旧服务端，旧链路应配合 HTTPS 使用。
	env.Path = cmd.SignedPath()
	errV2 := signer.Verify(a.cfg.Secret, cmd.Signature, env, 10*time.Minute)
	if errV2 == nil {
		return nil
	}
	env.Path = cmd.ID
	if errV1 := signer.Verify(a.cfg.Secret, cmd.Signature, env, 10*time.Minute); errV1 == nil {
		return nil
	}
	return errV2
}

func (a *Agent) executeUpgrade(ctx context.Context, cmd protocol.Command) {
	// script_url 优先于 Artifact.URL（与执行器保持一致），再按实际下载地址决定
	// 是否携带 Agent 凭据：只有平台相对路径才附带，避免泄露给第三方脚本仓库。
	// script 模式与 exec+script_url（只执行命令但脚本外置）都走这条路径。
	downloadURL := cmd.Payload.Artifact.URL
	if (cmd.Payload.Component.DeployMode == protocol.DeployScript ||
		cmd.Payload.Component.DeployMode == protocol.DeployExec) &&
		cmd.Payload.Component.ScriptURL != "" {
		downloadURL = cmd.Payload.Component.ScriptURL
	}
	artifactURL, downloadAuth := a.resolveArtifactURL(downloadURL)
	cmd.Payload.Artifact.URL = artifactURL

	// 进入维护窗口（申请租约，避免升级期间被判掉线）
	a.postStage(cmd.ID, protocol.StageRequest{
		AgentID: a.cfg.AgentID, Stage: protocol.StageMaintenance,
		Message: "进入维护窗口", ExpectedSeconds: 600,
	})

	res, err := executor.Execute(ctx, cmd, a.svcMgr, downloadAuth, func(stage, message string) {
		// 每个阶段都带详情上报，任务详情页无需等最终结果才能看到失败原因/脚本输出。
		a.postStage(cmd.ID, protocol.StageRequest{
			AgentID: a.cfg.AgentID, Stage: stage, Message: message,
		})
	})

	report := protocol.ReportRequest{
		AgentID: a.cfg.AgentID, ToVersion: cmd.Payload.ToVersion,
	}
	if res != nil {
		report.Stage = res.Stage
		report.Log = res.Log
		report.ExitCode = res.ExitCode
		report.ToVersion = res.ToVersion
	}
	if err != nil || res == nil || !res.Success {
		report.Status = protocol.CmdFailed
		// 错误不一定经过 executor 的 logf（例如 install_path 校验直接 return），
		// 保证最终状态和日志都能说明失败原因，而不是停在 done。
		report.Stage = protocol.StageFailed
		if err != nil {
			report.Log += fmt.Sprintf("[%s] 升级失败: %v\n", time.Now().Format("2006-01-02 15:04:05"), err)
			log.Printf("升级失败: %v", err)
		} else {
			report.Log += fmt.Sprintf("[%s] 升级失败: 执行器未返回成功结果\n", time.Now().Format("2006-01-02 15:04:05"))
			log.Printf("升级失败: 执行器未返回成功结果")
		}
	} else {
		report.Status = protocol.CmdSuccess
		report.Stage = protocol.StageDone
		log.Printf("升级成功: → %s", report.ToVersion)
	}
	_ = a.postReportWithRetry(cmd.ID, report)
}

// postStage 上报阶段与详情。阶段失败不影响升级执行，但会记录日志便于排查。
func (a *Agent) postStage(cmdID string, req protocol.StageRequest) {
	req.AgentID = a.cfg.AgentID
	req.Message = truncateText(req.Message, 4*1024)
	if err := a.postJSON("/api/agent/commands/"+cmdID+"/stage", req, nil); err != nil {
		log.Printf("⚠️ 阶段上报失败 cmd=%s stage=%s: %v", cmdID, req.Stage, err)
	}
}

// postReportWithRetry 上报最终结果：先重试，仍失败则写入 state，由心跳继续补报。
//
// 服务端按 cmdID + agentID 落库并对终态做幂等保护，重复上报不会把成功改成失败。
func (a *Agent) postReportWithRetry(cmdID string, report protocol.ReportRequest) error {
	report.AgentID = a.cfg.AgentID
	report.Log = truncateText(report.Log, maxAgentReportLogBytes)
	if a.cfg.PendingReports == nil {
		a.cfg.PendingReports = map[string]protocol.ReportRequest{}
	}
	// 极端情况下服务端长时间不可达，限制本地队列大小，避免 state 无限增长。
	for len(a.cfg.PendingReports) >= maxPendingReports {
		dropped := false
		for id := range a.cfg.PendingReports {
			if id != cmdID {
				delete(a.cfg.PendingReports, id)
				dropped = true
				break
			}
		}
		if !dropped {
			break
		}
	}
	a.cfg.PendingReports[cmdID] = report
	if err := a.saveState(); err != nil {
		log.Printf("⚠️ 保存待补报结果失败: %v", err)
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := a.postJSON("/api/agent/commands/"+cmdID+"/report", report, nil); err == nil {
			delete(a.cfg.PendingReports, cmdID)
			_ = a.saveState()
			return nil
		} else {
			lastErr = err
			log.Printf("⚠️ 结果上报失败 cmd=%s（第 %d 次）: %v", cmdID, attempt+1, err)
		}
		if attempt < 2 {
			time.Sleep(time.Duration(1<<attempt) * time.Second)
		}
	}
	log.Printf("⚠️ 结果已写入本地待补报队列，心跳将继续重试 cmd=%s", cmdID)
	return lastErr
}

// flushPendingReports 补报本地队列中的最终结果；心跳与启动时调用。
func (a *Agent) flushPendingReports() {
	if len(a.cfg.PendingReports) == 0 {
		return
	}
	changed := false
	flushed := 0
	for cmdID, report := range a.cfg.PendingReports {
		if flushed >= 5 {
			break
		}
		err := a.postJSON("/api/agent/commands/"+cmdID+"/report", report, nil)
		if err != nil {
			// 4xx 通常是服务端已删除指令或请求非法，继续重试没有意义。
			if strings.Contains(err.Error(), "HTTP 4") {
				log.Printf("⚠️ 放弃补报 cmd=%s: %v", cmdID, err)
				delete(a.cfg.PendingReports, cmdID)
				changed = true
				continue
			}
			log.Printf("⚠️ 补报结果失败 cmd=%s: %v", cmdID, err)
			break
		}
		delete(a.cfg.PendingReports, cmdID)
		changed = true
		flushed++
	}
	if changed {
		if err := a.saveState(); err != nil {
			log.Printf("⚠️ 保存待补报状态失败: %v", err)
		}
	}
}

// wasExecuted 判断本进程是否已执行过指定指令。
func (a *Agent) wasExecuted(cmdID string) bool {
	now := time.Now()
	for id, at := range a.executed {
		if now.Sub(at) > 24*time.Hour {
			delete(a.executed, id)
		}
	}
	_, ok := a.executed[cmdID]
	return ok
}

// markExecuted 记录已执行指令，避免至少一次投递导致重复升级。
func (a *Agent) markExecuted(cmdID string) {
	if a.executed == nil {
		a.executed = map[string]time.Time{}
	}
	a.executed[cmdID] = time.Now()
	if len(a.executed) > 1000 {
		// 简单限界：删除最早的一批，避免长期运行无限增长。
		cutoff := time.Now().Add(-24 * time.Hour)
		for id, at := range a.executed {
			if at.Before(cutoff) {
				delete(a.executed, id)
			}
		}
	}
}

// truncateText 在 UTF-8 字符边界上截断文本，避免把多字节字符切成乱码。
func truncateText(s string, maxBytes int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 {
		if s[cut]&0xC0 != 0x80 {
			break
		}
		cut--
	}
	return s[:cut]
}

func (a *Agent) postJSON(path string, req, resp any) error {
	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequest("POST", a.cfg.ServerURL+path, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	if a.cfg.Token != "" {
		// Agent 认证：Bearer <agent_id>:<token>（服务端校验 token 哈希）
		httpReq.Header.Set("Authorization", "Bearer "+a.cfg.AgentID+":"+a.cfg.Token)
	}
	httpResp, err := a.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", httpResp.StatusCode)
	}
	if resp != nil {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(httpResp.Body).Decode(&envelope); err != nil {
			return err
		}
		return json.Unmarshal(envelope.Data, resp)
	}
	return nil
}

func (a *Agent) getLocalIP() string {
	// 简化：返回 hostname 或留空由服务端从 RemoteAddr 提取
	return ""
}

// saveState 原子写入 Agent 状态。
//
// 先写同目录临时文件并 fsync，再 rename 覆盖，避免进程在写入中途被杀死时
// 留下 0 字节的 state.json（旧实现 os.WriteFile 会先截断目标文件）。
func (a *Agent) saveState() error {
	if a.cfg.StatePath == "" {
		return nil
	}
	data, err := json.Marshal(a.cfg)
	if err != nil {
		return err
	}
	dir := filepath.Dir(a.cfg.StatePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// state 内含 token/secret，目录必须仅 root 可访问。
	// 相对路径（dir="."）或根目录不做 chmod，避免误改当前目录/系统根目录。
	if dir != "." && dir != string(filepath.Separator) {
		_ = os.Chmod(dir, 0700)
	}

	tmp := a.cfg.StatePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if f, err := os.OpenFile(tmp, os.O_RDWR, 0600); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmp, a.cfg.StatePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// LoadState 加载已保存的凭据。
//
// 空文件视为「尚未注册」，不返回错误，便于安装器先写入 ServerURL 后由
// run 补写凭据；内容损坏时仍返回错误，由调用方提示重新安装。
func LoadState(path string) (Config, error) {
	var cfg Config
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}
	err = json.Unmarshal(data, &cfg)
	return cfg, err
}

// resolveArtifactURL 返回下载地址和应携带的认证头。
//
// 相对路径视为平台制品端点，补全 ServerURL 并附带 Agent 凭据；
// 绝对地址视为自定义下载源（如组件 script_url），不发送 Agent 凭据，
// 避免脚本托管在第三方仓库时泄露 Agent token。
func (a *Agent) resolveArtifactURL(raw string) (string, string) {
	if strings.HasPrefix(raw, "/") {
		return a.cfg.ServerURL + raw, a.authHeader()
	}
	return raw, ""
}

// authHeader 返回制品下载所用的认证头，与 Agent API 一致。
func (a *Agent) authHeader() string {
	if a.cfg.AgentID == "" || a.cfg.Token == "" {
		return ""
	}
	return "Bearer " + a.cfg.AgentID + ":" + a.cfg.Token
}

// exitIP 返回本机出口 IP（按访问服务端的路由选择网卡）。
//
// 探测失败时沿用上一次成功的值，避免多网卡主机在路由抖动时页面上地址反复清空。
func (a *Agent) exitIP() string {
	if ip := netinfo.ExitIPFromServerURL(a.cfg.ServerURL); ip != "" {
		a.cfg.ExitIP = ip
	}
	return a.cfg.ExitIP
}

// handleConnectFailure 处理连续心跳失败：覆盖地址先回退，默认地址再轮换备用。
//
// 场景一：管理员填了错误的「连接地址覆盖」→ Agent 切过去后连不上服务端，
// 也就收不到「清空覆盖」的心跳响应。连续 3 次（约 90 秒）失败即回退默认地址。
// 场景二：默认地址本身不可用（DNS/入口故障）→ 连续 2 次失败后轮换到下一个
// 备用地址（安装时 --server 传入的多个地址），避免必须依赖服务端下发才能恢复。
func (a *Agent) handleConnectFailure() {
	onDefault := a.cfg.DefaultServerURL == "" || a.cfg.ServerURL == a.cfg.DefaultServerURL
	if !onDefault {
		a.connectFailures++
		if a.connectFailures < 3 {
			return
		}
		log.Printf("⚠️ 连接地址连续 %d 次心跳失败，回退默认地址 %s", a.connectFailures, a.cfg.DefaultServerURL)
		from := a.cfg.ServerURL
		a.recordSwitch(from, fmt.Sprintf("覆盖地址连续 %d 次心跳失败，回退默认地址", a.connectFailures))
		a.tunnelFailURL = from
		a.tunnelFailAt = time.Now()
		a.cfg.ServerURL = a.cfg.DefaultServerURL
		a.cfg.AppliedOverride = ""
		a.connectFailures = 0
		if err := a.saveState(); err != nil {
			log.Printf("⚠️ 保存回退地址失败: %v", err)
		}
		return
	}

	a.connectFailures++
	if a.connectFailures < 2 {
		return
	}
	a.connectFailures = 0
	next := a.nextServerURL()
	if next == "" || next == a.cfg.ServerURL {
		return
	}
	log.Printf("🔁 默认地址不可达，切换备用地址: %s -> %s", a.cfg.ServerURL, next)
	from := a.cfg.ServerURL
	a.recordSwitch(from, "默认地址连续 2 次心跳失败，切换备用地址")
	a.cfg.ServerURL = next
	a.cfg.AppliedOverride = ""
	if err := a.saveState(); err != nil {
		log.Printf("⚠️ 保存备用地址失败: %v", err)
	}
}

// recordSwitch 记录最近一次连接地址切换，随 state 持久化供 status 展示。
func (a *Agent) recordSwitch(from, reason string) {
	a.cfg.LastSwitchFrom = from
	a.cfg.LastSwitchReason = reason
	a.cfg.LastSwitchAt = time.Now()
}

// nextServerURL 返回备用地址列表中的下一个地址（循环）。
func (a *Agent) nextServerURL() string {
	urls := a.cfg.ServerURLs
	if len(urls) < 2 {
		return ""
	}
	cur := strings.TrimRight(a.cfg.ServerURL, "/")
	for i, u := range urls {
		if strings.TrimRight(u, "/") == cur {
			return urls[(i+1)%len(urls)]
		}
	}
	return urls[0]
}

// normalizeServerList 去重并规范化备用地址列表。
//
// 当前地址始终包含在列表首位（若原本不在），保证轮换时有完整集合。
func normalizeServerList(current string, list []string) []string {
	seen := make(map[string]bool, len(list)+1)
	out := make([]string, 0, len(list)+1)
	add := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	add(current)
	for _, u := range list {
		add(u)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyServerURL 应用服务端下发的隧道地址，返回是否有变化。
//
// 目标地址：
//   - 响应为空：回退到安装时的默认地址（清空隧道配置后自动恢复）；
//   - 响应为「IP」或「IP:端口」：沿用默认地址的协议，按需补默认端口；
//   - 响应为完整 URL：直接使用。
func (a *Agent) applyServerURL(raw string) bool {
	next := a.resolveServerURL(raw)
	if next == "" || next == a.cfg.ServerURL {
		return false
	}
	// 刚失败过的隧道地址在冷却期内不再重试，避免连接来回抖动。
	if next == a.tunnelFailURL && time.Since(a.tunnelFailAt) < tunnelRetryAfter {
		return false
	}
	log.Printf("🔗 切换服务端地址: %s -> %s", a.cfg.ServerURL, next)
	from := a.cfg.ServerURL
	reason := "服务端下发连接地址覆盖"
	if strings.TrimSpace(raw) == "" {
		reason = "服务端清空覆盖，回退默认地址"
	}
	a.recordSwitch(from, reason)
	a.cfg.ServerURL = next
	if strings.TrimSpace(raw) == "" {
		a.cfg.AppliedOverride = ""
	} else {
		a.cfg.AppliedOverride = next
	}
	a.connectFailures = 0
	return true
}

// resolveServerURL 把服务端下发的隧道地址规范成完整的 http(s) URL。
func (a *Agent) resolveServerURL(raw string) string {
	fallback := a.cfg.DefaultServerURL
	if fallback == "" {
		fallback = a.cfg.ServerURL
	}
	if strings.TrimSpace(raw) == "" {
		return strings.TrimRight(fallback, "/")
	}
	next := hostnet.ResolveServerURL(raw, fallback)
	if next == "" {
		log.Printf("⚠️ 忽略非法的隧道地址: %q", raw)
	}
	return next
}

// startTunnelProbe 异步从主机侧拨测服务端下发的地址，结果缓存到下一次心跳上报。
//
// 同一地址 5 分钟内不重复拨测；拨测进行中不并发重入，避免心跳密集时压满主机。
func (a *Agent) startTunnelProbe(url string) {
	a.tunnelMu.Lock()
	if a.tunnelProbing {
		a.tunnelMu.Unlock()
		return
	}
	if url == a.tunnelLastURL && time.Since(a.tunnelLastAt) < 5*time.Minute {
		a.tunnelMu.Unlock()
		return
	}
	a.tunnelProbing = true
	a.tunnelLastURL = url
	a.tunnelLastAt = time.Now()
	a.tunnelMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res := tunnelprobe.ProbeWithClient(ctx, url, 8*time.Second, a.client)
		result := &protocol.TunnelCheckResult{
			URL: url, OK: res.OK, LatencyMS: res.LatencyMS,
		}
		if !res.OK {
			result.Detail = tunnelProbeDetail(res)
			log.Printf("⚠️ 主机侧隧道自检未通过 %s: %s", url, result.Detail)
		} else {
			log.Printf("✅ 主机侧隧道自检通过 %s（%dms）", url, res.LatencyMS)
		}
		a.tunnelMu.Lock()
		a.tunnelPending = result
		a.tunnelProbing = false
		a.tunnelMu.Unlock()
	}()
}

// tunnelProbeDetail 把拨测失败原因压缩成一行（服务端写入 tunnel_check_detail）。
func tunnelProbeDetail(res tunnelprobe.Result) string {
	parts := make([]string, 0, len(res.Stages)+1)
	for _, st := range res.Stages {
		if st.OK {
			continue
		}
		parts = append(parts, st.Name+": "+st.Detail)
	}
	if res.Hint != "" {
		parts = append(parts, res.Hint)
	}
	return strings.Join(parts, "；")
}
