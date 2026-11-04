package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteInitialStateWritesServerAndToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 使用 ACL，跳过 Unix 权限检查")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeInitialState(Options{StatePath: path, ServerURL: "https://mgr.example.com", EnrollToken: "tok-123", Force: false}); err != nil {
		t.Fatalf("writeInitialState: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 state: %v", err)
	}
	var st persistedState
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("解析 state: %v", err)
	}
	if st.ServerURL != "https://mgr.example.com" || st.EnrollToken != "tok-123" {
		t.Fatalf("state 内容错误: %+v", st)
	}
	if st.DefaultServerURL != "https://mgr.example.com" {
		t.Fatalf("DefaultServerURL 应写入安装地址（隧道回退用），got %q", st.DefaultServerURL)
	}
	if st.StatePath != path {
		t.Fatalf("StatePath = %q, want %q", st.StatePath, path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("state 权限 = %o, want 600", fi.Mode().Perm())
	}
}

func TestWriteInitialStateOverwritesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeInitialState(Options{StatePath: path, ServerURL: "https://mgr.example.com", EnrollToken: "tok-456", Force: false}); err != nil {
		t.Fatalf("writeInitialState: %v", err)
	}

	var st persistedState
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("解析 state: %v", err)
	}
	if st.EnrollToken != "tok-456" {
		t.Fatalf("EnrollToken = %q, want tok-456", st.EnrollToken)
	}
}

func TestWriteInitialStateKeepsRegisteredCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := `{"ServerURL":"https://old.example.com","AgentID":"agent-1","Token":"token-1","Secret":"secret-1"}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	if err := writeInitialState(Options{StatePath: path, ServerURL: "https://new.example.com", EnrollToken: "new-token", Force: false}); err != nil {
		t.Fatalf("writeInitialState: %v", err)
	}

	var st persistedState
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("解析 state: %v", err)
	}
	if st.AgentID != "agent-1" || st.Token != "token-1" || st.Secret != "secret-1" {
		t.Fatalf("已注册凭据被覆盖: %+v", st)
	}
	if st.ServerURL != "https://old.example.com" {
		t.Fatalf("ServerURL 被意外修改: %q", st.ServerURL)
	}
}

func TestWriteInitialStateForceOverwritesCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := `{"ServerURL":"https://old.example.com","AgentID":"agent-1","Token":"token-1","Secret":"secret-1"}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	if err := writeInitialState(Options{StatePath: path, ServerURL: "https://new.example.com", EnrollToken: "new-token", Force: true}); err != nil {
		t.Fatalf("writeInitialState: %v", err)
	}

	var st persistedState
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("解析 state: %v", err)
	}
	if st.AgentID != "" || st.Token != "" || st.Secret != "" {
		t.Fatalf("--force 应清空旧凭据，got %+v", st)
	}
	if st.ServerURL != "https://new.example.com" || st.EnrollToken != "new-token" {
		t.Fatalf("--force 应写入新服务端/令牌，got %+v", st)
	}
}

// TestSystemdTemplateIncludesLogFlags systemd 服务也必须显式传入日志参数。
func TestSystemdTemplateIncludesLogFlags(t *testing.T) {
	content, err := render("upgrade-agent.service.tmpl", Options{
		BinPath:    "/usr/local/bin/upgrade-agent",
		ServerURL:  "http://mgr.example.com:8090",
		StatePath:  "/var/lib/upgrade-agent/state.json",
		LogPath:    "/var/log/upgrade-agent.log",
		LogMaxMB:   10,
		LogBackups: 5,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"--log /var/log/upgrade-agent.log",
		"--log-max-mb 10",
		"--log-backups 5",
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("systemd 模板缺少 %q:\n%s", want, content)
		}
	}
}

// TestUpstartTemplateIncludesLogFlags upstart 模板必须带文件日志参数，由 Agent 自切割。
func TestUpstartTemplateIncludesLogFlags(t *testing.T) {
	content, err := render("upgrade-agent.conf.tmpl", Options{
		BinPath:    "/usr/local/bin/upgrade-agent",
		ServerURL:  "http://mgr.example.com:8090",
		StatePath:  "/var/lib/upgrade-agent/state.json",
		LogPath:    "/var/log/upgrade-agent.log",
		LogMaxMB:   10,
		LogBackups: 5,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"--log /var/log/upgrade-agent.log",
		"--log-max-mb 10",
		"--log-backups 5",
		"respawn limit 10 5",
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("upstart 模板缺少 %q:\n%s", want, content)
		}
	}
	if strings.Contains(string(content), ">>/var/log/upgrade-agent.log") {
		t.Fatalf("upstart 不应再重定向 stderr，避免与 Agent 文件日志重复:\n%s", content)
	}
}

// TestInstallRejectsUnitInjection 服务单元模板直接写入 --server 参数：
// 必须拒绝换行/空白/非 http(s) 值，避免向 unit 文件注入额外指令。
func TestInstallRejectsUnitInjection(t *testing.T) {
	base := t.TempDir()
	cases := []string{
		"http://example.com/\n[Service]\nExecStart=/bin/sh",
		"http://example.com/ with space",
		"javascript:alert(1)",
		"file:///etc/passwd",
	}
	for _, s := range cases {
		err := Install(Options{
			ServerURL: s,
			BinPath:   filepath.Join(base, "agent"),
			StatePath: filepath.Join(base, "state.json"),
		})
		if err == nil {
			t.Fatalf("server=%q 应被拒绝", s)
		}
	}
}
