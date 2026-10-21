package agentrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewTrimsServerURL(t *testing.T) {
	a := New(Config{ServerURL: "http://10.0.88.88:8090/"})
	if a.cfg.ServerURL != "http://10.0.88.88:8090" {
		t.Fatalf("ServerURL = %q, want %q", a.cfg.ServerURL, "http://10.0.88.88:8090")
	}
}

func TestLoadStateEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadState(path)
	if err != nil {
		t.Fatalf("空 state 不应返回错误: %v", err)
	}
	if cfg.AgentID != "" || cfg.EnrollToken != "" {
		t.Fatalf("空 state 应得到空配置，got %+v", cfg)
	}
}

func TestLoadStateMissingFile(t *testing.T) {
	_, err := LoadState(filepath.Join(t.TempDir(), "missing.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("缺失 state 应返回 IsNotExist，got %v", err)
	}
}

func TestRunRejectsMissingEnrollToken(t *testing.T) {
	a := New(Config{
		ServerURL: "http://127.0.0.1:1",
		StatePath: filepath.Join(t.TempDir(), "state.json"),
	})
	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "缺少注册令牌") {
		t.Fatalf("缺少令牌应直接报错，got %v", err)
	}
}

func TestSaveStateAtomicAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a := New(Config{
		ServerURL: "https://mgr.example.com",
		AgentID:   "agent-1",
		Token:     "token-1",
		Secret:    "secret-1",
		StatePath: path,
	})
	if err := a.saveState(); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("state 文件不应为空")
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("解析 state: %v", err)
	}
	if cfg.AgentID != "agent-1" || cfg.Token != "token-1" || cfg.Secret != "secret-1" {
		t.Fatalf("state 内容错误: %+v", cfg)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("state 权限 = %o, want 600", fi.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("临时文件不应残留: %v", err)
	}
}

func TestResolveArtifactURL(t *testing.T) {
	a := New(Config{ServerURL: "http://10.0.88.88:8090/", AgentID: "agent-1", Token: "tok"})

	u, auth := a.resolveArtifactURL("/api/artifacts/1/download")
	if u != "http://10.0.88.88:8090/api/artifacts/1/download" {
		t.Fatalf("平台制品 URL = %q", u)
	}
	if auth != "Bearer agent-1:tok" {
		t.Fatalf("平台制品应携带 Agent 凭据，got %q", auth)
	}

	u, auth = a.resolveArtifactURL("https://repo.example.com/install.sh")
	if u != "https://repo.example.com/install.sh" {
		t.Fatalf("自定义 URL = %q", u)
	}
	if auth != "" {
		t.Fatalf("自定义 script_url 不应携带 Agent 凭据，got %q", auth)
	}
}
