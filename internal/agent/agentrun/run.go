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
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/agent/executor"
	"github.com/iflyelf/upgrade_mgr/internal/agent/osinfo"
	"github.com/iflyelf/upgrade_mgr/internal/agent/selfupdate"
	"github.com/iflyelf/upgrade_mgr/internal/agent/svcmgr"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/signer"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// Config Agent 运行配置
type Config struct {
	ServerURL     string
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
}

const (
	maxPendingReports      = 20
	maxAgentReportLogBytes = 64 * 1024
)

// New 创建 Agent
func New(cfg Config) *Agent {
	// 容忍 --server 末尾多写斜杠，避免请求路径变成 //api/...
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	info := osinfo.Detect()
	if cfg.PendingReports == nil {
		cfg.PendingReports = map[string]protocol.ReportRequest{}
	}
	return &Agent{
		cfg:       cfg,
		info:      info,
		svcMgr:    svcmgr.New(info.Init),
		client:    &http.Client{Timeout: 60 * time.Second},
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
			return ctx.Err()
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
		Arch:           a.info.Arch,
		Kernel:         a.info.Kernel,
		Supervisor:     a.info.Init,
		AgentVersion:   "0.1.0",
		BuildVersion:   a.cfg.Version,
		BuildHash:      a.buildHash,
		BuildVariant:   a.cfg.Variant,
		Components:     map[string]string{},
		SignatureModes: protocol.SupportedSignatureModes(),
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
	req := protocol.HeartbeatRequest{
		AgentID:        a.cfg.AgentID,
		Busy:           false,
		Status:         "online",
		BuildVersion:   a.cfg.Version,
		BuildHash:      a.buildHash,
		BuildVariant:   a.cfg.Variant,
		SignatureModes: protocol.SupportedSignatureModes(),
	}
	_ = a.postJSON("/api/agent/heartbeat", req, nil)
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
	case protocol.CmdSelfUpdate:
		a.executeSelfUpdate(ctx, cmd)
	default:
		log.Printf("暂不支持的指令类型: %s", cmd.Type)
	}
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
	downloadURL := cmd.Payload.Artifact.URL
	if cmd.Payload.Component.DeployMode == protocol.DeployScript && cmd.Payload.Component.ScriptURL != "" {
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
