// Package protocol 定义控制台与 Agent 之间的通信数据结构（共享类型）。
//
// 通信模型：Agent 主动出站 HTTP 长轮询取指令；至少一次投递 + cmd_id 幂等。
// 指令信封由服务端用 per-agent 密钥 HMAC 签名，Agent 校验（见 pkg/signer）。
package protocol

import (
	"strconv"
	"time"
)

// ---- 指令类型 ----

const (
	CmdUpgradeComponent   = "upgrade_component"
	CmdRollbackComponent  = "rollback_component"
	CmdSelfUpdate         = "self_update"
	CmdUninstallComponent = "uninstall_component"
	CmdResync             = "resync"
	CmdHealthCheck        = "health_check"
	CmdRestartAgent       = "restart_agent"
)

// ---- 部署模式 ----

const (
	DeployReplace     = "replace"
	DeployExec        = "exec"
	DeployReplaceExec = "replace_exec"
	// DeployScript 下载制品后作为 Shell 脚本执行，适合“安装脚本即制品”的场景。
	DeployScript = "script"
)

// ---- 指令状态 ----

const (
	CmdPending    = "pending"
	CmdDispatched = "dispatched"
	CmdAccepted   = "accepted"
	CmdRunning    = "running"
	CmdSuccess    = "success"
	CmdFailed     = "failed"
	CmdRolledBack = "rolled_back"
	CmdTimeout    = "timeout"
	CmdExpired    = "expired"
	CmdCanceled   = "canceled"
)

// ---- 阶段（用于进度展示与维护租约申请）----

const (
	StageAccepted    = "accepted"
	StageMaintenance = "enter_maintenance"
	StageDownloading = "downloading"
	StageDownloaded  = "downloaded"
	StagePreInstall  = "pre_install"
	StageBackup      = "backup"
	StageStopping    = "stopping"
	StageReplacing   = "replacing"
	StageExec        = "exec"
	StageStarting    = "starting"
	StageVerifying   = "verifying"
	StagePostInstall = "post_install"
	StageRollback    = "rollback"
	StageDone        = "done"
)

// RegisterRequest Agent 首次注册（携带一次性 enroll token）。
type RegisterRequest struct {
	EnrollToken  string            `json:"enroll_token"`
	Hostname     string            `json:"hostname"`
	IP           string            `json:"ip"`
	OS           string            `json:"os"` // os_family，如 centos/ubuntu
	OSVersion    string            `json:"os_version"`
	Arch         string            `json:"arch"` // amd64/arm64
	Kernel       string            `json:"kernel"`
	Supervisor   string            `json:"supervisor"` // systemd/upstart/cron/bare
	AgentVersion string            `json:"agent_version"`
	Components   map[string]string `json:"components"` // 组件名 → 当前版本
}

// RegisterResponse 注册结果，返回长期 agent 凭据。
type RegisterResponse struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`  // 后续请求鉴权
	Secret  string `json:"secret"` // 指令信封 HMAC 校验密钥
}

// HeartbeatRequest 心跳（含忙碌状态与本机组件版本）。
type HeartbeatRequest struct {
	AgentID    string            `json:"agent_id"`
	Busy       bool              `json:"busy"`
	RunningCmd string            `json:"running_cmd,omitempty"`
	Stage      string            `json:"stage,omitempty"`
	Status     string            `json:"status,omitempty"` // online/upgrading/...
	Components map[string]string `json:"components,omitempty"`
}

// PollRequest 长轮询取指令。
type PollRequest struct {
	AgentID string `json:"agent_id"`
	Busy    bool   `json:"busy"`
}

// Artifact 制品下发信息（Agent 据此下载校验替换）。
type Artifact struct {
	ID        int64  `json:"id"`
	Version   string `json:"version"`
	Arch      string `json:"arch"`
	OSFamily  string `json:"os_family"`
	URL       string `json:"url"`
	MD5       string `json:"md5"`
	SHA256    string `json:"sha256,omitempty"`
	Size      int64  `json:"size"`
	InnerPath string `json:"inner_path,omitempty"` // 压缩包内二进制路径
}

// ComponentSpec 组件部署规格（下发给 Agent 执行）。
type ComponentSpec struct {
	Name        string   `json:"name"`
	DeployMode  string   `json:"deploy_mode"`
	InstallPath string   `json:"install_path"`
	BackupDir   string   `json:"backup_dir"`
	ExecPath    string   `json:"exec_path,omitempty"`
	ExecArgs    []string `json:"exec_args,omitempty"`
	ExecEnv     []string `json:"exec_env,omitempty"`
	WorkDir     string   `json:"work_dir,omitempty"`
	ExecTimeout string   `json:"exec_timeout,omitempty"`
	// ScriptURL 是 script 模式的自定义脚本下载地址（绝对 http/https）。
	// 非空时服务端不再查找制品，Agent 直接从该地址下载并执行。
	ScriptURL   string     `json:"script_url,omitempty"`
	IgnoreError bool       `json:"ignore_error,omitempty"`
	PreInstall  [][]string `json:"pre_install,omitempty"`
	PostInstall [][]string `json:"post_install,omitempty"`
	// PostInstallScript 为 post_install 阶段执行的脚本内容（Agent 写入临时文件后执行）。
	PostInstallScript string     `json:"post_install_script,omitempty"`
	ServiceName       string     `json:"service_name,omitempty"`
	ServiceMgr        string     `json:"service_mgr,omitempty"`
	StopCmd           [][]string `json:"stop_cmd,omitempty"`
	StartCmd          [][]string `json:"start_cmd,omitempty"`
	RestartCmd        [][]string `json:"restart_cmd,omitempty"`
	VersionCmd        [][]string `json:"version_cmd,omitempty"`
	VersionRegex      string     `json:"version_regex,omitempty"`
	HealthCmd         [][]string `json:"health_cmd,omitempty"`
	ConsulMaint       bool       `json:"consul_maint,omitempty"`
}

// CommandPayload 指令载荷。
type CommandPayload struct {
	Component  ComponentSpec `json:"component"`
	Artifact   Artifact      `json:"artifact"`
	ToVersion  string        `json:"to_version"`
	RollbackTo string        `json:"rollback_to,omitempty"`
}

// Command 下发给 Agent 的指令信封。
type Command struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Payload   CommandPayload `json:"payload"`
	Timestamp int64          `json:"timestamp"`
	Nonce     string         `json:"nonce"`
	Signature string         `json:"signature"`
	ExpireAt  time.Time      `json:"expire_at"`
}

// SignedPath 返回参与 HMAC 签名的指令标识：ID、Type、ExpireAt 均纳入签名，
// 防止中间人篡改指令类型（如把 upgrade 改为 self_update）或延长过期时间。
func (c Command) SignedPath() string {
	return c.ID + "|" + c.Type + "|" + strconv.FormatInt(c.ExpireAt.Unix(), 10)
}

// PollResponse 长轮询响应。HasCommand=false 表示本轮无指令。
type PollResponse struct {
	HasCommand bool     `json:"has_command"`
	Command    *Command `json:"command,omitempty"`
}

// StageRequest 阶段上报（enter_maintenance 时申请维护租约）。
type StageRequest struct {
	AgentID         string `json:"agent_id"`
	Stage           string `json:"stage"`
	ExpectedSeconds int    `json:"expected_seconds,omitempty"`
	WillRestart     bool   `json:"will_restart,omitempty"`
}

// ReportRequest 结果/进度上报（可多次，最终态含 result）。
type ReportRequest struct {
	AgentID     string `json:"agent_id"`
	Status      string `json:"status"` // running/success/failed/rolled_back/timeout
	Stage       string `json:"stage,omitempty"`
	FromVersion string `json:"from_version,omitempty"`
	ToVersion   string `json:"to_version,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`
	Log         string `json:"log,omitempty"`
}

// AckRequest 指令接收确认。
type AckRequest struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"` // accepted / rejected
	Reason  string `json:"reason,omitempty"`
}
