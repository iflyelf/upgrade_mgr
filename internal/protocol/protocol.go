// Package protocol 定义控制台与 Agent 之间的通信数据结构（共享类型）。
//
// 通信模型：Agent 主动出站 HTTP 长轮询取指令；至少一次投递 + cmd_id 幂等。
// 指令信封由服务端用 per-agent 密钥 HMAC 签名，Agent 校验（见 pkg/signer）。
package protocol

import (
	"bytes"
	"strconv"
	"strings"
	"time"
)

// ---- Agent 指令签名能力 ----
//
// Agent 注册/心跳时上报自己可校验的签名方式，服务端据此选择旧版 signature
// 使用哪条 HMAC Path，避免依赖版本号推断。未上报的旧 Agent 默认只支持 id。
const (
	// SignatureModeID 旧版 legacy 签名：Path 仅指令 ID。
	SignatureModeID = "id"
	// SignatureModeSignedPath 新版 legacy 签名：Path 为 ID|类型|过期时间。
	SignatureModeSignedPath = "signed_path"
	// SignatureModeV2 支持 signature_v2 字段（Path 为 SignedPath，优先校验）。
	SignatureModeV2 = "v2"
)

// SupportedSignatureModes 返回当前 Agent 支持的签名能力（按稳定顺序）。
func SupportedSignatureModes() []string {
	return []string{SignatureModeID, SignatureModeSignedPath, SignatureModeV2}
}

// NormalizeSignatureModes 过滤未知值、去重并按稳定顺序返回。
// 未知能力直接忽略，避免新 Agent 上报未来能力时影响旧服务端解析。
func NormalizeSignatureModes(modes []string) []string {
	set := make(map[string]bool, len(modes))
	for _, raw := range modes {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case SignatureModeID:
			set[SignatureModeID] = true
		case SignatureModeSignedPath:
			set[SignatureModeSignedPath] = true
		case SignatureModeV2:
			set[SignatureModeV2] = true
		}
	}
	out := make([]string, 0, 3)
	for _, mode := range SupportedSignatureModes() {
		if set[mode] {
			out = append(out, mode)
		}
	}
	return out
}

// ---- Agent 能力标识 ----
//
// Agent 注册/心跳时上报功能能力，服务端在下方指令前据此判断兼容性，
// 避免旧 Agent 收到不认识的指令类型后静默丢弃、任务卡到租约超时。
const (
	// AgentCapabilityRunCommand 支持 run_command（一次性脚本/命令）指令。
	AgentCapabilityRunCommand = "run_command"
	// AgentCapabilityOptionalCommand 指令载荷中的 command 字段可省略（指针 + omitempty）。
	//
	// 历史坑：v0.x（512d1f9）把 CommandPayload.Command 写成值类型，encoding/json
	// 不会省略零值结构体，导致所有指令载荷都带 "command":{"script":""}；
	// 旧 Agent 不认识该字段，验签重序列化时丢弃它 → 「签名不匹配」。
	// 服务端据此能力区分：上报 run_command 但没上报本能力的老 Agent 需要按旧序列化签名。
	AgentCapabilityOptionalCommand = "optional_command"
	// AgentCapabilityUninstallDetect 执行器能识别卸载操作（exec_args 首个非 flag 参数为 uninstall/remove），
	// 脚本执行后服务已删除时跳过启服，避免卸载示例填 service_name 导致启服失败。
	// 旧 Agent 不支持此能力时，服务端自动从卸载指令中剥离 service_name。
	AgentCapabilityUninstallDetect = "uninstall_detect"
)

// SupportedCapabilities 返回当前 Agent 支持的能力列表（按稳定顺序）。
func SupportedCapabilities() []string {
	return []string{AgentCapabilityRunCommand, AgentCapabilityOptionalCommand, AgentCapabilityUninstallDetect}
}

// NormalizeCapabilities 过滤未知能力、去重并按稳定顺序返回。
func NormalizeCapabilities(caps []string) []string {
	set := make(map[string]bool, len(caps))
	for _, raw := range caps {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case AgentCapabilityRunCommand:
			set[AgentCapabilityRunCommand] = true
		case AgentCapabilityOptionalCommand:
			set[AgentCapabilityOptionalCommand] = true
		case AgentCapabilityUninstallDetect:
			set[AgentCapabilityUninstallDetect] = true
		}
	}
	out := make([]string, 0, len(set))
	for _, c := range SupportedCapabilities() {
		if set[c] {
			out = append(out, c)
		}
	}
	return out
}

// IsUninstallAction 判断 exec_args 是否表示卸载：首个非 flag 参数为 uninstall/remove/purge。
//
// Agent 据此在卸载后跳过启服；服务端据此给不支持 uninstall_detect 的旧 Agent
// 剥离 service_name，两侧必须使用同一判断。
func IsUninstallAction(args []string) bool {
	for _, arg := range args {
		a := strings.ToLower(strings.TrimSpace(arg))
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		return a == "uninstall" || a == "remove" || a == "purge"
	}
	return false
}

// HasCapability 判断逗号分隔的能力字符串是否包含指定能力。
func HasCapability(caps, want string) bool {
	for _, raw := range strings.Split(caps, ",") {
		if strings.EqualFold(strings.TrimSpace(raw), want) {
			return true
		}
	}
	return false
}

// PreferredLegacySignatureMode 从能力列表中选择旧版 signature 的签名路径。
// 同时支持两种旧路径时优先 SignedPath（覆盖类型/过期时间，强度更高）。
// 未上报能力时回退到 id，兼容早期只认 ID 签名的 Agent。
func PreferredLegacySignatureMode(modes []string) string {
	for _, mode := range NormalizeSignatureModes(modes) {
		if mode == SignatureModeSignedPath {
			return SignatureModeSignedPath
		}
	}
	return SignatureModeID
}

// ---- 指令类型 ----

const (
	CmdUpgradeComponent   = "upgrade_component"
	CmdRollbackComponent  = "rollback_component"
	CmdSelfUpdate         = "self_update"
	CmdUninstallComponent = "uninstall_component"
	// CmdRunCommand 执行一次性 Shell 脚本（不涉及制品、备份与回滚）。
	CmdRunCommand   = "run_command"
	CmdResync       = "resync"
	CmdHealthCheck  = "health_check"
	CmdRestartAgent = "restart_agent"
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
	// StageRunCommand 一次性命令任务的执行阶段（与组件“执行安装”区分）。
	StageRunCommand  = "run_command"
	StageStarting    = "starting"
	StageVerifying   = "verifying"
	StagePostInstall = "post_install"
	StageRollback    = "rollback"
	// StageFailed 表示执行失败的最终阶段。旧实现错误复用了 StageDone，
	// 导致界面把“下载失败/停服失败”也显示成“完成”。
	StageFailed = "failed"
	StageDone   = "done"
)

// RegisterRequest Agent 首次注册（携带一次性 enroll token）。
type RegisterRequest struct {
	EnrollToken string `json:"enroll_token"`
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	// ExitIP 是 Agent 按路由探测到的本机出口 IP（多网卡时用于展示与筛选）。
	// 服务端不信任该字段作为主机身份，仅校验格式后入库。
	ExitIP       string            `json:"exit_ip,omitempty"`
	OS           string            `json:"os"` // os_family，如 centos/ubuntu
	OSVersion    string            `json:"os_version"`
	Arch         string            `json:"arch"` // amd64/arm64
	Kernel       string            `json:"kernel"`
	Supervisor   string            `json:"supervisor"` // systemd/upstart/cron/bare
	AgentVersion string            `json:"agent_version"`
	Components   map[string]string `json:"components"` // 组件名 → 当前版本
	// BuildVersion/BuildHash/BuildVariant 用于服务端判断 Agent 是否需要自升级。
	// BuildHash 是 Agent 对自身可执行文件计算的 SHA256；旧 Agent 不上报（空值）。
	BuildVersion string `json:"build_version,omitempty"`
	BuildHash    string `json:"build_hash,omitempty"`
	BuildVariant string `json:"build_variant,omitempty"` // "" 普通构建 / legacy 旧内核构建
	// SignatureModes 声明可校验的指令签名能力，取值见 SignatureMode*。
	// 旧 Agent 不发送该字段，服务端按 id 兼容处理。
	SignatureModes []string `json:"signature_modes,omitempty"`
	// Capabilities 声明支持的功能能力（run_command 等），旧 Agent 不发送。
	Capabilities []string `json:"capabilities,omitempty"`
}

// RegisterResponse 注册结果，返回长期 agent 凭据。
type RegisterResponse struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`  // 后续请求鉴权
	Secret  string `json:"secret"` // 指令信封 HMAC 校验密钥
}

// TunnelCheckResult 主机侧回连自检结果（随心跳上报）。
//
// Agent 收到心跳响应中的 tunnel_check_url 后，从主机侧拨测该地址，
// 把结果放在下一次心跳里回报，服务端写入 hosts.tunnel_check_*。
type TunnelCheckResult struct {
	URL       string `json:"url"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Detail    string `json:"detail,omitempty"`
}

// HeartbeatRequest 心跳（含忙碌状态与本机组件版本）。
type HeartbeatRequest struct {
	AgentID string `json:"agent_id"`
	Busy    bool   `json:"busy"`
	// ExitIP 本机出口 IP；AgentServerURL 是 Agent 当前实际连接的服务端地址，
	// AgentDefaultServerURL 是安装时的默认地址。两者用于确认隧道切换是否
	// 已生效，并让服务端补齐隧道地址缺失的协议/端口。旧 Agent 不上报。
	ExitIP                string            `json:"exit_ip,omitempty"`
	AgentServerURL        string            `json:"agent_server_url,omitempty"`
	AgentDefaultServerURL string            `json:"agent_default_server_url,omitempty"`
	RunningCmd            string            `json:"running_cmd,omitempty"`
	Stage                 string            `json:"stage,omitempty"`
	Status                string            `json:"status,omitempty"` // online/upgrading/...
	Components            map[string]string `json:"components,omitempty"`
	// 原地自升级后，新 Agent 通过心跳刷新构建信息；旧 Agent 不发送，保持数据库原值。
	BuildVersion string `json:"build_version,omitempty"`
	BuildHash    string `json:"build_hash,omitempty"`
	BuildVariant string `json:"build_variant,omitempty"`
	// SignatureModes 与注册时一致；用于 Agent 原地升级后刷新签名能力。
	SignatureModes []string `json:"signature_modes,omitempty"`
	// Capabilities 与注册时一致；服务端据此刷新功能能力。
	Capabilities []string `json:"capabilities,omitempty"`
	// TunnelCheck 主机侧回连自检结果（上一轮被要求拨测时上报）。
	TunnelCheck *TunnelCheckResult `json:"tunnel_check,omitempty"`
}

// HeartbeatResponse 心跳响应。
//
// ServerURL 非空时表示管理员为该主机配置了隧道地址（hosts.tunnel_ip），
// Agent 应立即切换并持久化，后续心跳/轮询/下载都走该地址；
// 为空表示未配置隧道，Agent 若此前切换过，应回退到安装时的默认地址。
type HeartbeatResponse struct {
	ServerURL string `json:"server_url,omitempty"`
	// ClockSkew 是 Agent 校验指令签名时间戳时允许的时钟偏移窗口（如 "5m"）。
	// 旧服务端不发送时 Agent 保持默认 10m，保证向后兼容。
	ClockSkew string `json:"clock_skew,omitempty"`
	// TunnelCheckURL 非空时要求 Agent 从主机侧拨测该地址，并在下一次心跳回报结果。
	// 服务端仅在保存隧道地址后的一段时间内下发，避免持续占用主机资源。
	TunnelCheckURL string `json:"tunnel_check_url,omitempty"`
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

// CommandSpec 一次性脚本指令规格（kind=command 任务下发）。
//
// 与组件升级不同：不下载制品、不做平台备份、不停服、不回滚；
// 只把脚本写入临时文件后用 Shell 执行，并回传输出。
type CommandSpec struct {
	// Script 脚本内容（由服务端保存的任务定义下发）。
	Script string `json:"script"`
	// Interpreter 解释器，空则 bash/sh 自动选择。
	Interpreter string `json:"interpreter,omitempty"`
	// Args 传给脚本的参数（$1、$2…）。
	Args []string `json:"args,omitempty"`
	// Env 追加环境变量（KEY=VALUE）。
	Env []string `json:"env,omitempty"`
	// WorkDir 工作目录。
	WorkDir string `json:"work_dir,omitempty"`
	// Timeout 执行超时，如 "10m"；空则默认 10 分钟。
	Timeout string `json:"timeout,omitempty"`
	// Stdin 标准输入内容（如 "Y\n"），用于需要交互确认的安装/卸载脚本。
	Stdin string `json:"stdin,omitempty"`
	// TTY 为 true 时通过 script(1) 分配伪终端，兼容必须读取 /dev/tty 的脚本。
	TTY bool `json:"tty,omitempty"`
	// IgnoreError 为 true 时非零退出码也视为成功。
	IgnoreError bool `json:"ignore_error,omitempty"`
}

// CommandPayload 指令载荷。
//
// 注意：Command 必须是指针 + omitempty。encoding/json 不会省略零值结构体，
// 若用值类型，所有指令（含 self_update/upgrade）载荷都会多出
// "command":{"script":""}；旧 Agent 不认识该字段、验签重序列化时丢弃它，
// 会直接导致「签名不匹配」。
type CommandPayload struct {
	Component  ComponentSpec `json:"component"`
	Artifact   Artifact      `json:"artifact"`
	ToVersion  string        `json:"to_version"`
	RollbackTo string        `json:"rollback_to,omitempty"`
	// Command 仅 run_command 指令使用。
	Command *CommandSpec `json:"command,omitempty"`
}

// CanonicalPayloadWithLegacyCommand 在 canonical payload 末尾补上旧版值类型
// Command 字段序列化出的 `"command":{"script":""}`。
//
// 背景：v0.x（512d1f9）把 CommandPayload.Command 写成值类型 CommandSpec 且带
// omitempty。encoding/json 不会省略零值结构体，因此该版本 Agent 在验签时会对
// 所有指令载荷补出 `"command":{"script":""}`。服务端对这类 Agent 必须签同样的
// 字节，否则会持续回报「签名不匹配」，连自我升级都发不出去。
func CanonicalPayloadWithLegacyCommand(canonical []byte) []byte {
	if len(canonical) <= 2 || canonical[len(canonical)-1] != '}' {
		return canonical
	}
	if bytes.Contains(canonical, []byte(`"command"`)) {
		return canonical
	}
	out := make([]byte, 0, len(canonical)+len(legacyCommandFragment)-1)
	out = append(out, canonical[:len(canonical)-1]...)
	out = append(out, legacyCommandFragment...)
	return out
}

var legacyCommandFragment = []byte(`,"command":{"script":""}}`)

// Command 下发给 Agent 的指令信封。
type Command struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Payload   CommandPayload `json:"payload"`
	Timestamp int64          `json:"timestamp"`
	Nonce     string         `json:"nonce"`
	// Signature 旧版签名：Path 只含指令 ID。
	// 服务端必须继续下发该字段，旧 Agent 才能在不重装的情况下工作。
	Signature string `json:"signature"`
	// SignatureV2 新版签名：Path 为 SignedPath()（ID|类型|过期时间）。
	// 新 Agent 优先校验；旧 Agent 会忽略该未知字段。
	SignatureV2 string    `json:"signature_v2,omitempty"`
	ExpireAt    time.Time `json:"expire_at"`
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
	AgentID string `json:"agent_id"`
	Stage   string `json:"stage"`
	// Message 是阶段详情（下载地址、脚本输出、错误原因等）。
	// 旧 Agent 不发送，服务端只能显示阶段名称；新 Agent 会携带以便任务详情排查。
	Message         string `json:"message,omitempty"`
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
