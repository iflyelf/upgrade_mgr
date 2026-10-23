package agentrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildStatusUnregistered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := BuildStatus(path, "1.2.3", "")
	if err != nil {
		t.Fatalf("缺失 state 不应报错: %v", err)
	}
	if st.Registered {
		t.Fatal("空 state 应为未注册")
	}
	if st.StatePath != path {
		t.Fatalf("StatePath=%q", st.StatePath)
	}
	if st.AddressSource != "" {
		t.Fatalf("无地址时来源应为空: %q", st.AddressSource)
	}
	if !strings.Contains(st.FormatText(), "未注册") {
		t.Fatalf("文本应提示未注册:\n%s", st.FormatText())
	}
}

func TestBuildStatusWithOverrideAndSwitch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	hb := time.Now().Add(-30 * time.Second).Truncate(time.Second)
	sw := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	// 注意：state 由 agentrun.saveState 用 Go 默认字段名序列化（ServerURL/AgentID…），
	// 只有少数字段带 json tag。这里按真实格式构造，避免测试与线上不一致。
	extra := map[string]any{
		"ServerURL":             "http://10.8.0.5:8090",
		"default_server_url":    "http://10.0.0.5:8090",
		"server_urls":           []string{"http://10.0.0.5:8090", "http://10.8.0.5:8090"},
		"applied_override":      "http://10.8.0.5:8090",
		"requested_override":    "http://10.8.0.5:8090",
		"requested_override_at": sw,
		"AgentID":               "agent-1",
		"Token":                 "token-1",
		"Secret":                "secret-1",
		"StatePath":             path,
		"exit_ip":               "192.0.2.10",
		"last_heartbeat":        hb,
		"crash_count":           2,
		"pending_reports":       map[string]any{"cmd-1": map[string]any{"status": "failed"}},
		"last_switch_from":      "http://10.0.0.5:8090",
		"last_switch_reason":    "服务端下发连接地址覆盖",
		"last_switch_at":        sw,
		"tls_cert":              "/etc/agent.crt",
		"tls_key":               "/etc/agent.key",
	}
	data, _ := json.Marshal(extra)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	st, err := BuildStatus(path, "1.2.3", "legacy")
	if err != nil {
		t.Fatalf("BuildStatus: %v", err)
	}
	if !st.Registered || st.AgentID != "agent-1" {
		t.Fatalf("注册状态错误: %+v", st)
	}
	if st.AddressSource != AddrSourceOverride {
		t.Fatalf("覆盖地址来源判断错误: %q", st.AddressSource)
	}
	if st.CrashCount != 2 || st.PendingCount != 1 {
		t.Fatalf("崩溃/待补报统计错误: crash=%d pending=%d", st.CrashCount, st.PendingCount)
	}
	if st.LastSwitch == nil || st.LastSwitch.From != "http://10.0.0.5:8090" {
		t.Fatalf("最近切换缺失: %+v", st.LastSwitch)
	}
	if !st.TLSEnabled || !st.TLSClientCert {
		t.Fatalf("TLS 状态错误: %+v", st)
	}
	if st.RequestedOverride != "http://10.8.0.5:8090" || !st.OverrideApplied {
		t.Fatalf("覆盖对比错误: requested=%q applied=%v", st.RequestedOverride, st.OverrideApplied)
	}
	if st.RequestedAt == nil {
		t.Fatal("应记录服务端下发时间")
	}

	txt := st.FormatText()
	for _, want := range []string{"当前地址", "默认地址", "备用地址", "最近切换", "原因", "legacy", "服务端覆盖", "已生效"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("文本缺少 %q:\n%s", want, txt)
		}
	}
	// 绝不能输出凭据
	if strings.Contains(txt, "token-1") || strings.Contains(txt, "secret-1") {
		t.Fatalf("status 泄露凭据:\n%s", txt)
	}
	js := st.FormatJSON()
	if strings.Contains(js, "token-1") || strings.Contains(js, "secret-1") {
		t.Fatalf("JSON 泄露凭据:\n%s", js)
	}
}

func TestAddressSourceDefaultAndOverride(t *testing.T) {
	if got := addressSource("http://a", "http://a", nil, ""); got != AddrSourceDefault {
		t.Fatalf("默认地址判断错误: %q", got)
	}
	if got := addressSource("http://b", "http://a", []string{"http://a"}, ""); got != AddrSourceOverride {
		t.Fatalf("覆盖地址判断错误: %q", got)
	}
	if got := addressSource("http://c", "http://a", []string{"http://a", "http://c"}, ""); got != AddrSourceFallback {
		t.Fatalf("备用地址判断错误: %q", got)
	}
	// applied_override 精确匹配时优先判定为覆盖（即使也在备用列表里）
	if got := addressSource("http://c", "http://a", []string{"http://a", "http://c"}, "http://c"); got != AddrSourceOverride {
		t.Fatalf("applied_override 应优先: %q", got)
	}
	if got := addressSource("", "http://a", nil, ""); got != "" {
		t.Fatalf("空地址应返回空: %q", got)
	}
}

// TestOverrideApplied 覆盖对比：一致、仅 IP、不一致、未下发。
func TestOverrideApplied(t *testing.T) {
	cases := []struct {
		cur, req string
		want     bool
	}{
		{"http://10.8.0.5:8090", "http://10.8.0.5:8090", true},
		{"http://10.8.0.5:8090", "http://10.8.0.5:8090/", true},
		{"http://10.8.0.5:8090", "10.8.0.5", false},
		{"http://10.8.0.5:8090", "http://10.9.0.5:8090", false},
		{"http://10.0.0.5:8090", "", true},
		{"", "", false},
	}
	for _, c := range cases {
		if got := overrideApplied(c.cur, c.req); got != c.want {
			t.Errorf("overrideApplied(%q,%q)=%v want %v", c.cur, c.req, got, c.want)
		}
	}
}
