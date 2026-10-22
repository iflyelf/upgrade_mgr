// Package agentrun Agent 主循环：注册→心跳→长轮询取指令→执行→上报。
package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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
}

// Agent 主体
type Agent struct {
	cfg    Config
	info   osinfo.Info
	svcMgr svcmgr.Manager
	client *http.Client
}

// New 创建 Agent
func New(cfg Config) *Agent {
	info := osinfo.Detect()
	return &Agent{
		cfg:    cfg,
		info:   info,
		svcMgr: svcmgr.New(info.Init),
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// Run 运行主循环
func (a *Agent) Run(ctx context.Context) error {
	// 1. 注册（如果还没有凭据）
	if a.cfg.AgentID == "" {
		if err := a.register(); err != nil {
			return fmt.Errorf("注册失败: %w", err)
		}
		a.saveState()
		log.Printf("注册成功: agent_id=%s", a.cfg.AgentID)
	}

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
		SignatureModes: protocol.SupportedSignatureModes(),
	}
	_ = a.postJSON("/api/agent/heartbeat", req, nil)
	a.cfg.LastHeartbeat = time.Now()
	a.saveState()
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

	// ACK
	_ = a.postJSON("/api/agent/commands/"+cmd.ID+"/ack",
		protocol.AckRequest{AgentID: a.cfg.AgentID, Status: "accepted"}, nil)

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
	// 自升级：下载→备份→替换→exit(75)，由 systemd/upstart 重启拉起新版本
	log.Printf("执行自升级到版本 %s", cmd.Payload.ToVersion)

	binPath, _ := os.Executable()
	if err := selfupdate.Execute(ctx, cmd, binPath, a.authHeader()); err != nil {
		log.Printf("自升级失败: %v", err)
		report := protocol.ReportRequest{
			AgentID: a.cfg.AgentID, Status: protocol.CmdFailed,
			Log: fmt.Sprintf("自升级失败: %v", err),
		}
		_ = a.postJSON("/api/agent/commands/"+cmd.ID+"/report", report, nil)
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
	// 也可能只签了 ID。旧链路应配合 HTTPS 使用。
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
	// 把制品相对 URL 补全为绝对地址
	if strings.HasPrefix(cmd.Payload.Artifact.URL, "/") {
		cmd.Payload.Artifact.URL = a.cfg.ServerURL + cmd.Payload.Artifact.URL
	}

	// 进入维护窗口（申请租约，避免升级期间被判掉线）
	_ = a.postJSON("/api/agent/commands/"+cmd.ID+"/stage",
		protocol.StageRequest{AgentID: a.cfg.AgentID, Stage: protocol.StageMaintenance, ExpectedSeconds: 600}, nil)

	res, err := executor.Execute(ctx, cmd, a.svcMgr, a.authHeader(), func(stage string) {
		_ = a.postJSON("/api/agent/commands/"+cmd.ID+"/stage",
			protocol.StageRequest{AgentID: a.cfg.AgentID, Stage: stage}, nil)
	})

	report := protocol.ReportRequest{AgentID: a.cfg.AgentID, ToVersion: cmd.Payload.ToVersion}
	if res != nil {
		report.Stage = res.Stage
		report.Log = res.Log
		report.ExitCode = res.ExitCode
		report.ToVersion = res.ToVersion
	}
	if err != nil || (res != nil && !res.Success) {
		report.Status = protocol.CmdFailed
		log.Printf("升级失败: %v", err)
	} else {
		report.Status = protocol.CmdSuccess
		log.Printf("升级成功: → %s", report.ToVersion)
	}
	_ = a.postJSON("/api/agent/commands/"+cmd.ID+"/report", report, nil)
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

func (a *Agent) saveState() {
	if a.cfg.StatePath == "" {
		return
	}
	data, _ := json.Marshal(a.cfg)
	_ = os.WriteFile(a.cfg.StatePath, data, 0600)
}

// LoadState 加载已保存的凭据
func LoadState(path string) (Config, error) {
	var cfg Config
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(data, &cfg)
	return cfg, err
}

// authHeader 返回制品下载所用的认证头，与 Agent API 一致。
func (a *Agent) authHeader() string {
	if a.cfg.AgentID == "" || a.cfg.Token == "" {
		return ""
	}
	return "Bearer " + a.cfg.AgentID + ":" + a.cfg.Token
}
