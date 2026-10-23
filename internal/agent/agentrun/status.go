package agentrun

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentpath"
	"github.com/iflyelf/upgrade_mgr/internal/agent/osinfo"
)

// Status 现场排查用的 Agent 状态快照（`upgrade-agent status`）。
//
// 只包含网络/连接/构建等排障信息，绝不包含 token/secret 等凭据。
type Status struct {
	OS            osinfo.Info `json:"os"`
	Version       string      `json:"version"`
	Variant       string      `json:"variant,omitempty"`
	StatePath     string      `json:"state_path"`
	Registered    bool        `json:"registered"`
	AgentID       string      `json:"agent_id,omitempty"`
	ServerURL     string      `json:"server_url"`
	DefaultURL    string      `json:"default_server_url"`
	AddressSource string      `json:"address_source"`
	ServerURLs    []string    `json:"server_urls,omitempty"`
	ExitIP        string      `json:"exit_ip,omitempty"`
	LastHeartbeat *time.Time  `json:"last_heartbeat,omitempty"`
	CrashCount    int         `json:"crash_count"`
	PendingCount  int         `json:"pending_reports"`
	LastSwitch    *SwitchInfo `json:"last_switch,omitempty"`
	// 服务端下发的覆盖地址与本机生效地址的对比。
	RequestedOverride string     `json:"requested_override,omitempty"`
	RequestedAt       *time.Time `json:"requested_at,omitempty"`
	OverrideApplied   bool       `json:"override_applied"`
	TLSEnabled        bool       `json:"tls_enabled"`
	TLSClientCert     bool       `json:"tls_client_cert"`
}

// SwitchInfo 最近一次连接地址切换。
type SwitchInfo struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// 地址来源取值。
const (
	AddrSourceDefault  = "默认地址"
	AddrSourceOverride = "服务端覆盖"
	AddrSourceFallback = "备用地址"
)

// BuildStatus 读取 state 并组装状态快照。
//
// statePath 为空时使用平台默认路径；version/variant 为编译期注入值。
// 文件不存在或为空不视为错误：返回「未注册」状态，便于新机器排查。
func BuildStatus(statePath, version, variant string) (Status, error) {
	if statePath == "" {
		statePath = agentpath.StatePath()
	}
	cfg, err := LoadState(statePath)
	if err != nil && !os.IsNotExist(err) {
		return Status{}, err
	}
	// crash_count 由 watchdog 维护，不在 Config 结构里；单独读取一次。
	crashCount := 0
	if b, readErr := os.ReadFile(statePath); readErr == nil && len(b) > 0 {
		var extra struct {
			CrashCount int `json:"crash_count"`
		}
		if json.Unmarshal(b, &extra) == nil {
			crashCount = extra.CrashCount
		}
	}
	cur := firstNonEmpty(cfg.ServerURL, cfg.DefaultServerURL)
	def := firstNonEmpty(cfg.DefaultServerURL, cfg.ServerURL)
	// 展示时保留配置顺序（安装时填写的顺序），只做去重与补全。
	urls := displayServerList(cur, cfg.ServerURLs)

	st := Status{
		OS:                osinfo.Detect(),
		Version:           version,
		Variant:           variant,
		StatePath:         statePath,
		Registered:        cfg.AgentID != "",
		AgentID:           cfg.AgentID,
		ServerURL:         cur,
		DefaultURL:        def,
		AddressSource:     addressSource(cur, def, urls, cfg.AppliedOverride),
		ServerURLs:        urls,
		ExitIP:            cfg.ExitIP,
		CrashCount:        crashCount,
		PendingCount:      len(cfg.PendingReports),
		TLSEnabled:        cfg.TLSCA != "" || cfg.TLSCert != "",
		TLSClientCert:     cfg.TLSCert != "" && cfg.TLSKey != "",
		RequestedOverride: cfg.RequestedOverride,
		OverrideApplied:   overrideApplied(cur, cfg.RequestedOverride),
	}
	if !cfg.RequestedOverrideAt.IsZero() {
		at := cfg.RequestedOverrideAt
		st.RequestedAt = &at
	}
	if !cfg.LastHeartbeat.IsZero() {
		hb := cfg.LastHeartbeat
		st.LastHeartbeat = &hb
	}
	if !cfg.LastSwitchAt.IsZero() {
		st.LastSwitch = &SwitchInfo{
			From:   cfg.LastSwitchFrom,
			To:     cur,
			Reason: cfg.LastSwitchReason,
			At:     cfg.LastSwitchAt,
		}
	}
	return st, nil
}

// overrideApplied 判断服务端要求与本机生效地址是否一致。
//
// 服务端未下发（空）时视为「无覆盖要求」，本机应处于默认地址才算一致。
func overrideApplied(current, requested string) bool {
	cur := strings.TrimRight(strings.TrimSpace(current), "/")
	req := strings.TrimRight(strings.TrimSpace(requested), "/")
	if req == "" {
		return cur != ""
	}
	if req == cur {
		return true
	}
	// 服务端可能只下发 IP（由 Agent 补协议/端口），用后缀主机比较兜底。
	return strings.Contains(req, "://") && strings.HasSuffix(cur, req)
}

// displayServerList 去重并保留配置顺序，确保当前地址在列表中。
func displayServerList(current string, list []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(list)+1)
	appendIfNew := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	for _, u := range list {
		appendIfNew(u)
	}
	appendIfNew(current)
	return out
}

// addressSource 判断当前地址来自默认、服务端覆盖还是备用列表。
//
// 优先使用 state 中记录的 applied_override（精确），再退回列表匹配。
func addressSource(cur, def string, urls []string, appliedOverride string) string {
	cur = strings.TrimRight(cur, "/")
	def = strings.TrimRight(def, "/")
	if cur == "" {
		return ""
	}
	if cur == def {
		return AddrSourceDefault
	}
	if appliedOverride != "" && strings.TrimRight(appliedOverride, "/") == cur {
		return AddrSourceOverride
	}
	for _, u := range urls {
		if strings.TrimRight(u, "/") == cur {
			return AddrSourceFallback
		}
	}
	return AddrSourceOverride
}

// FormatText 把状态渲染成便于现场阅读的文本。
func (s Status) FormatText() string {
	var b strings.Builder
	const labelWidth = 12
	line := func(k, v string) {
		if v == "" {
			return
		}
		fmt.Fprintf(&b, "%s%s\n", padLabel(k+":", labelWidth), v)
	}
	line("版本", s.Version+ifVariant(s.Variant))
	line("系统", fmt.Sprintf("%s %s (%s)", s.OS.OSName, s.OS.OSVersion, s.OS.Arch))
	line("init/服务", s.OS.Init)
	line("出口 IP", s.ExitIP)
	line("状态文件", s.StatePath)
	if s.Registered {
		line("注册状态", "已注册（agent_id="+s.AgentID+"）")
	} else {
		line("注册状态", "未注册（state 缺少凭据，请重新执行 install）")
	}
	if s.ServerURL != "" {
		src := s.AddressSource
		if src == "" {
			src = AddrSourceDefault
		}
		line("当前地址", s.ServerURL+"（"+src+"）")
	}
	line("默认地址", s.DefaultURL)
	if len(s.ServerURLs) > 1 {
		line("备用地址", strings.Join(s.ServerURLs, "  →  "))
	}
	if s.LastHeartbeat != nil {
		line("最近心跳", fmt.Sprintf("%s（%s前）",
			s.LastHeartbeat.Format("2006-01-02 15:04:05"),
			shortDuration(time.Since(*s.LastHeartbeat))))
	}
	if s.CrashCount > 0 {
		line("连续崩溃", fmt.Sprintf("%d 次", s.CrashCount))
	}
	if s.PendingCount > 0 {
		line("待补报", fmt.Sprintf("%d 条（网络恢复后自动补报）", s.PendingCount))
	}
	if s.RequestedOverride != "" || s.RequestedAt != nil {
		reqText := s.RequestedOverride
		if reqText == "" {
			reqText = "（服务端未下发覆盖）"
		}
		status := "已生效"
		if !s.OverrideApplied {
			status = "未生效（等待下一次心跳或地址不可达）"
		}
		line("服务端覆盖", reqText)
		line("覆盖状态", status+overrideAtText(s.RequestedAt))
	}
	if s.LastSwitch != nil {
		fmt.Fprintf(&b, "%s%s → %s\n", padLabel("最近切换:", labelWidth),
			orDash(s.LastSwitch.From), orDash(s.LastSwitch.To))
		fmt.Fprintf(&b, "%s%s（%s）\n", padLabel("  原因:", labelWidth), s.LastSwitch.Reason,
			s.LastSwitch.At.Format("2006-01-02 15:04:05"))
	}
	if s.TLSEnabled {
		line("TLS", "已启用私有 CA"+ifClientCert(s.TLSClientCert))
	}
	return b.String()
}

// FormatJSON 输出 JSON（便于脚本采集）。
func (s Status) FormatJSON() string {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

// padLabel 按终端显示宽度左对齐（CJK 字符占 2 列）。
func padLabel(s string, width int) string {
	w := 0
	for _, r := range s {
		if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
			(r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe6f) ||
			(r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6)) {
			w += 2
		} else {
			w++
		}
	}
	if w >= width {
		return s + " "
	}
	return s + strings.Repeat(" ", width-w)
}

// overrideAtText 附加服务端下发时间，便于判断是否为新配置。
func overrideAtText(at *time.Time) string {
	if at == nil {
		return ""
	}
	return "（下发于 " + at.Format("2006-01-02 15:04:05") + "）"
}

func ifVariant(v string) string {
	if v == "" {
		return ""
	}
	return " (" + v + ")"
}

func ifClientCert(ok bool) string {
	if !ok {
		return ""
	}
	return " + 客户端证书"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d小时", int(d.Hours()))
	default:
		return fmt.Sprintf("%d天", int(d.Hours()/24))
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
