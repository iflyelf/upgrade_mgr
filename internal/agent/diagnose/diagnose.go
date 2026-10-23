// Package diagnose 主动诊断 Agent 与服务端之间的完整链路。
//
// 与 status 的区别：status 只读本地 state；diagnose 会真正发起网络请求，
// 依次检查 DNS → TCP → TLS → HTTP /health，并（可选）做一次带凭据的心跳，
// 输出每一步的耗时，便于判断「连不上」是网络、证书还是认证问题。
//
// 心跳默认不执行：心跳会让服务端更新 last_seen，开启 agent.auto_update 时
// 还可能触发自升级。需要验证认证链路时显式加 --heartbeat。
package diagnose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentpath"
	"github.com/iflyelf/upgrade_mgr/internal/agent/agentrun"
	"github.com/iflyelf/upgrade_mgr/internal/agent/installer"
	"github.com/iflyelf/upgrade_mgr/internal/agent/osinfo"
	"github.com/iflyelf/upgrade_mgr/internal/agent/svcmgr"
	"github.com/iflyelf/upgrade_mgr/internal/agent/tlsclient"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/hostnet"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/netinfo"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/tunnelprobe"
)

// DefaultTimeout 单个网络阶段的默认超时。
const DefaultTimeout = 5 * time.Second

// Options 诊断参数。
type Options struct {
	// StatePath 状态文件路径；为空使用平台默认值。
	StatePath string
	// ServerURL 覆盖待测试地址（不写回 state）；为空则使用 state 中的当前地址。
	ServerURL string
	// Timeout 单个网络阶段超时。
	Timeout time.Duration
	// Heartbeat 是否执行一次带凭据的心跳（可能触发服务端自动升级）。
	Heartbeat bool
	// Version/Variant 编译期注入，仅用于展示。
	Version string
	Variant string
}

// Stage 单个诊断步骤。
type Stage struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	// Warn 表示该步骤不影响整体结论（例如 Agent 服务未以服务方式运行）。
	Warn      bool   `json:"warn,omitempty"`
	Detail    string `json:"detail"`
	LatencyMS int64  `json:"latency_ms"`
}

// Result 诊断结果。
type Result struct {
	StartedAt  time.Time   `json:"started_at"`
	TotalMS    int64       `json:"total_ms"`
	OK         bool        `json:"ok"`
	Version    string      `json:"version"`
	Variant    string      `json:"variant,omitempty"`
	OS         osinfo.Info `json:"os"`
	Registered bool        `json:"registered"`
	ServerURL  string      `json:"server_url"`
	DefaultURL string      `json:"default_server_url"`
	ExitIP     string      `json:"exit_ip,omitempty"`
	Stages     []Stage     `json:"stages"`
	// Warnings 不影响整体结论的提醒（如 Agent 未以常驻服务方式运行）。
	Warnings []string `json:"warnings,omitempty"`
	Hint     string   `json:"hint,omitempty"`
}

// Run 执行诊断。
func Run(opts Options) (*Result, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.StatePath == "" {
		opts.StatePath = agentpath.StatePath()
	}
	// 默认视为通过；任何一个关键阶段失败都会置为 false。
	res := &Result{
		StartedAt: time.Now(), OK: true,
		Version: opts.Version, Variant: opts.Variant, Stages: []Stage{},
	}
	start := time.Now()

	// 1. state
	stepStart := time.Now()
	cfg, err := agentrun.LoadState(opts.StatePath)
	if err != nil && !os.IsNotExist(err) {
		res.Stages = append(res.Stages, Stage{
			Name: "读取状态", OK: false, Detail: err.Error(),
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
		res.OK = false
		res.Hint = "state 文件损坏：删除后重新执行 install 可恢复"
		res.TotalMS = time.Since(start).Milliseconds()
		return res, nil
	}
	res.Registered = cfg.AgentID != "" && cfg.Token != "" && cfg.Secret != ""
	switch {
	case res.Registered:
		res.Stages = append(res.Stages, Stage{
			Name: "读取状态", OK: true, Detail: "已注册（agent_id=" + cfg.AgentID + "）",
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
	case cfg.AgentID == "":
		res.Stages = append(res.Stages, Stage{
			Name: "读取状态", OK: true, Detail: "未注册（state 缺少凭据）",
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
	default:
		res.Stages = append(res.Stages, Stage{
			Name: "读取状态", OK: false, Detail: "凭据残缺（缺少 token/secret）",
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
	}

	// 2. 系统信息
	info := osinfo.Detect()
	res.OS = info
	res.Stages = append(res.Stages, Stage{
		Name: "系统信息", OK: true,
		Detail: fmt.Sprintf("%s %s (%s, %s)", info.OSName, info.OSVersion, info.Arch, info.Init),
	})

	// 3. 服务状态
	stepStart = time.Now()
	mgr := svcmgr.New(info.Init)
	if st, statusErr := mgr.Status(installer.ServiceName); statusErr != nil {
		res.Stages = append(res.Stages, Stage{
			Name: "服务状态", OK: false, Detail: statusErr.Error(),
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
	} else {
		good := strings.Contains(st, "running") || strings.Contains(st, "online")
		detail := strings.TrimSpace(st)
		if detail == "" {
			detail = "unknown"
		}
		// 前台手工运行时服务本来就不存在，不能因此判定整条链路失败；标记为警告。
		res.Stages = append(res.Stages, Stage{
			Name: "服务状态", OK: good, Warn: !good,
			Detail:    installer.ServiceName + ": " + detail,
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
		if !good {
			res.Warnings = append(res.Warnings,
				"Agent 服务未运行（前台运行时属正常）：如期望常驻请检查 systemd/SCM 状态与日志")
		}
	}

	// 4. 地址
	cur := strings.TrimSpace(opts.ServerURL)
	if cur == "" {
		cur = strings.TrimSpace(cfg.ServerURL)
	}
	if cur == "" {
		cur = strings.TrimSpace(cfg.DefaultServerURL)
	}
	def := strings.TrimSpace(cfg.DefaultServerURL)
	res.ServerURL = cur
	res.DefaultURL = def
	if cur == "" {
		res.Stages = append(res.Stages, Stage{
			Name: "连接地址", OK: false, Detail: "未配置服务端地址（state 与 --server 均为空）",
		})
		res.OK = false
		res.Hint = "执行 install --server <地址> 写入连接信息"
		res.TotalMS = time.Since(start).Milliseconds()
		return res, nil
	}
	// 与心跳一致：服务端可能只下发 IP，这里按默认地址补齐协议/端口。
	resolved := hostnet.ResolveServerURL(cur, def)
	if resolved == "" {
		resolved = cur
	}
	res.ServerURL = resolved

	// 5. 出口 IP
	stepStart = time.Now()
	if ip := netinfo.ExitIPFromServerURL(resolved); ip != "" {
		res.ExitIP = ip
		res.Stages = append(res.Stages, Stage{
			Name: "出口 IP", OK: true, Detail: ip,
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
	} else {
		res.Stages = append(res.Stages, Stage{
			Name: "出口 IP", OK: true, Detail: "未探测到（不影响连接）",
			LatencyMS: time.Since(stepStart).Milliseconds(),
		})
	}

	// 6. 网络拨测（DNS/TCP/TLS/HTTP）
	client, clientErr := tlsclient.New(tlsclient.Config{
		CAFile: cfg.TLSCA, CertFile: cfg.TLSCert, KeyFile: cfg.TLSKey,
	})
	if clientErr != nil {
		res.Stages = append(res.Stages, Stage{
			Name: "TLS 配置", OK: false, Detail: clientErr.Error(),
		})
		res.OK = false
		res.Hint = "检查 --tls-ca / --tls-cert / --tls-key 路径与权限"
		res.TotalMS = time.Since(start).Milliseconds()
		return res, nil
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), opts.Timeout*4)
	defer cancel()
	probe := tunnelprobe.ProbeWithClient(probeCtx, resolved, opts.Timeout, client)
	for _, st := range probe.Stages {
		res.Stages = append(res.Stages, Stage{
			Name: st.Name, OK: st.OK, Detail: st.Detail, LatencyMS: st.LatencyMS,
		})
	}
	if !probe.OK {
		res.OK = false
		if probe.Hint != "" {
			res.Hint = probe.Hint
		}
	}

	// 7. 可选：带凭据心跳（验证认证与服务端处理链路）
	if !opts.Heartbeat {
		res.Stages = append(res.Stages, Stage{
			Name: "鉴权心跳", OK: true, Skipped: true,
			Detail: "已跳过（加 --heartbeat 执行；可能触发服务端自动升级）",
		})
	} else if !res.Registered {
		res.Stages = append(res.Stages, Stage{
			Name: "鉴权心跳", OK: false, Detail: "未注册，无法验证认证链路",
		})
		res.OK = false
	} else {
		stepStart = time.Now()
		hbErr := sendHeartbeat(probeCtx, client, resolved, cfg.AgentID, cfg.Token, opts.Timeout)
		lat := time.Since(stepStart).Milliseconds()
		if hbErr != nil {
			res.Stages = append(res.Stages, Stage{
				Name: "鉴权心跳", OK: false, Detail: hbErr.Error(), LatencyMS: lat,
			})
			res.OK = false
			if strings.Contains(hbErr.Error(), "401") {
				res.Hint = "认证失败：state 中的 token 可能已失效，请重新注册（--force）"
			} else if strings.Contains(hbErr.Error(), "404") {
				res.Hint = "服务端不是本平台，或反向代理路径未转发 /api/agent"
			}
		} else {
			res.Stages = append(res.Stages, Stage{
				Name: "鉴权心跳", OK: true, Detail: "服务端已接受心跳", LatencyMS: lat,
			})
		}
	}

	res.TotalMS = time.Since(start).Milliseconds()
	if res.OK && res.Hint == "" {
		res.Hint = "链路正常；如仍有问题请查看 Agent 日志与任务详情"
	}
	return res, nil
}

// sendHeartbeat 发送一次最小心跳，验证认证与服务端链路。
func sendHeartbeat(ctx context.Context, client *http.Client, baseURL, agentID, token string, timeout time.Duration) error {
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	body, _ := json.Marshal(map[string]string{"status": "online"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/api/agent/heartbeat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentID+":"+token)

	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := client.Do(req.WithContext(ctx2))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// FormatText 渲染诊断结果。
func (r Result) FormatText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "诊断时间: %s\n", r.StartedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "版本:     %s\n", r.Version+ifVariant(r.Variant))
	fmt.Fprintf(&b, "服务端:   %s\n", r.ServerURL)
	if r.DefaultURL != "" && r.DefaultURL != r.ServerURL {
		fmt.Fprintf(&b, "默认地址: %s\n", r.DefaultURL)
	}
	if r.ExitIP != "" {
		fmt.Fprintf(&b, "出口 IP:  %s\n", r.ExitIP)
	}
	fmt.Fprintf(&b, "系统:     %s %s (%s, %s)\n\n", r.OS.OSName, r.OS.OSVersion, r.OS.Arch, r.OS.Init)

	for _, st := range r.Stages {
		mark := "✓"
		switch {
		case st.Skipped:
			mark = "-"
		case st.Warn:
			mark = "!"
		case !st.OK:
			mark = "✗"
		}
		latency := ""
		if st.LatencyMS > 0 {
			latency = fmt.Sprintf(" (%dms)", st.LatencyMS)
		}
		fmt.Fprintf(&b, "[%s] %s%s  %s\n", mark, st.Name, latency, st.Detail)
	}
	fmt.Fprintf(&b, "\n总耗时: %dms\n", r.TotalMS)
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "提醒:   %s\n", w)
	}
	if r.Hint != "" {
		fmt.Fprintf(&b, "结论:   %s\n", r.Hint)
	}
	return b.String()
}

// FormatJSON 输出 JSON。
func (r Result) FormatJSON() string {
	buf, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(buf)
}

func ifVariant(v string) string {
	if v == "" {
		return ""
	}
	return " (" + v + ")"
}
