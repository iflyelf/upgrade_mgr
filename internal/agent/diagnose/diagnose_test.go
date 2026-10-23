package diagnose

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newHealthServer 返回一个模拟平台的 /health 与心跳端点。
func newHealthServer(t *testing.T, rejectAuth bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok","service":"upgrade_mgr"}`))
		case "/api/agent/heartbeat":
			if rejectAuth {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"server_url":""}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func writeState(t *testing.T, dir string, extra map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, "state.json")
	base := map[string]any{
		"ServerURL":          "",
		"default_server_url": "",
		"AgentID":            "",
		"Token":              "",
		"Secret":             "",
	}
	for k, v := range extra {
		base[k] = v
	}
	b, _ := json.Marshal(base)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunNoState 未配置地址时给出明确提示。
func TestRunNoState(t *testing.T) {
	dir := t.TempDir()
	path := writeState(t, dir, nil)
	res, err := Run(Options{StatePath: path, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.OK {
		t.Fatal("无地址不应判定成功")
	}
	if !strings.Contains(res.Hint, "install") {
		t.Fatalf("应提示先安装: %q", res.Hint)
	}
}

// TestRunFullChain 健康服务端：网络阶段全部通过，未注册时心跳标记失败。
func TestRunFullChain(t *testing.T) {
	srv := newHealthServer(t, false)
	defer srv.Close()

	dir := t.TempDir()
	path := writeState(t, dir, map[string]any{"ServerURL": srv.URL})

	res, err := Run(Options{StatePath: path, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatalf("应诊断通过: %+v", res.Stages)
	}
	var names []string
	for _, st := range res.Stages {
		names = append(names, st.Name)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"读取状态", "系统信息", "服务状态", "TCP", "HTTP /health", "鉴权心跳"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少阶段 %q: %v", want, names)
		}
	}
	txt := res.FormatText()
	if !strings.Contains(txt, "总耗时") {
		t.Fatalf("文本缺少总耗时:\n%s", txt)
	}
}

// TestRunAuthenticatedHeartbeat 已注册时心跳成功。
func TestRunAuthenticatedHeartbeat(t *testing.T) {
	srv := newHealthServer(t, false)
	defer srv.Close()

	dir := t.TempDir()
	path := writeState(t, dir, map[string]any{
		"ServerURL": srv.URL,
		"AgentID":   "agent-1",
		"Token":     "tok",
		"Secret":    "sec",
	})

	res, err := Run(Options{StatePath: path, Timeout: 3 * time.Second, Heartbeat: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatalf("心跳应成功: %+v", res.Stages)
	}
	if !res.Registered {
		t.Fatal("应识别为已注册")
	}
}

// TestRunHeartbeatRejected 服务端返回 401 时给出重新注册提示。
func TestRunHeartbeatRejected(t *testing.T) {
	srv := newHealthServer(t, true)
	defer srv.Close()

	dir := t.TempDir()
	path := writeState(t, dir, map[string]any{
		"ServerURL": srv.URL,
		"AgentID":   "agent-1",
		"Token":     "tok",
		"Secret":    "sec",
	})

	res, err := Run(Options{StatePath: path, Timeout: 3 * time.Second, Heartbeat: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.OK {
		t.Fatal("401 不应判定成功")
	}
	if !strings.Contains(res.Hint, "重新注册") {
		t.Fatalf("应提示重新注册: %q", res.Hint)
	}
}

// TestRunServerOverride 指定 --server 时不读取 state 地址。
func TestRunServerOverride(t *testing.T) {
	srv := newHealthServer(t, false)
	defer srv.Close()

	dir := t.TempDir()
	path := writeState(t, dir, map[string]any{"ServerURL": "http://127.0.0.1:1"})

	res, err := Run(Options{StatePath: path, ServerURL: srv.URL, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK {
		t.Fatalf("覆盖地址应可用: %+v", res.Stages)
	}
	if res.ServerURL != srv.URL {
		t.Fatalf("ServerURL=%q", res.ServerURL)
	}
}
