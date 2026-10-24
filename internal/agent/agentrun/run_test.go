package agentrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/signer"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

func TestNewTrimsServerURL(t *testing.T) {
	a := New(Config{ServerURL: "http://10.0.88.88:8090/"})
	if a.cfg.ServerURL != "http://10.0.88.88:8090" {
		t.Fatalf("ServerURL = %q, want %q", a.cfg.ServerURL, "http://10.0.88.88:8090")
	}
	if a.cfg.ClockSkew != "10m" {
		t.Fatalf("ClockSkew = %q, want default 10m", a.cfg.ClockSkew)
	}
}

func TestNewKeepsValidClockSkew(t *testing.T) {
	a := New(Config{ServerURL: "http://127.0.0.1:8090", ClockSkew: "3m"})
	if a.cfg.ClockSkew != "3m" {
		t.Fatalf("ClockSkew = %q, want 3m", a.cfg.ClockSkew)
	}
}

func TestApplyClockSkew(t *testing.T) {
	a := New(Config{ServerURL: "http://127.0.0.1:8090"})
	if !a.applyClockSkew("3m") || a.cfg.ClockSkew != "3m" {
		t.Fatalf("合法配置未应用: %q", a.cfg.ClockSkew)
	}
	if a.applyClockSkew("bad") || a.cfg.ClockSkew != "3m" {
		t.Fatalf("非法配置不应覆盖当前值: %q", a.cfg.ClockSkew)
	}
}

func TestVerifyCommandUsesConfiguredClockSkew(t *testing.T) {
	secret := "test-secret"
	cmd := protocol.Command{
		ID:        "skew-1",
		Type:      protocol.CmdUpgradeComponent,
		Payload:   protocol.CommandPayload{ToVersion: "2.0.0"},
		Timestamp: time.Now().Add(-2 * time.Minute).Unix(),
		Nonce:     "nonce-skew",
		ExpireAt:  time.Now().Add(5 * time.Minute),
	}
	body, _ := json.Marshal(cmd.Payload)
	cmd.SignatureV2 = signer.Sign(secret, signer.Envelope{
		Method: "CMD", Path: cmd.SignedPath(), Body: body,
		Timestamp: cmd.Timestamp, Nonce: cmd.Nonce,
	})

	if err := New(Config{Secret: secret, ClockSkew: "1m"}).verifyCommand(cmd); err == nil {
		t.Fatal("2 分钟前的签名不应通过 1m 窗口")
	}
	if err := New(Config{Secret: secret, ClockSkew: "3m"}).verifyCommand(cmd); err != nil {
		t.Fatalf("2 分钟前的签名应通过 3m 窗口: %v", err)
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

func TestVerifyCommandUsesSignedPath(t *testing.T) {
	secret := "test-secret"
	cmd := protocol.Command{
		ID:        "1",
		Type:      protocol.CmdUpgradeComponent,
		Payload:   protocol.CommandPayload{ToVersion: "2.0.0"},
		Timestamp: time.Now().Unix(),
		Nonce:     "nonce-1",
		ExpireAt:  time.Now().Add(5 * time.Minute).Truncate(time.Second),
	}
	body, _ := json.Marshal(cmd.Payload)
	cmd.Signature = signer.Sign(secret, signer.Envelope{
		Method: "CMD", Path: cmd.SignedPath(), Body: body,
		Timestamp: cmd.Timestamp, Nonce: cmd.Nonce,
	})

	a := New(Config{Secret: secret})
	if err := a.verifyCommand(cmd); err != nil {
		t.Fatalf("正确签名应通过: %v", err)
	}

	// 类型也参与签名：篡改类型必须验签失败。
	cmd.Type = protocol.CmdSelfUpdate
	if err := a.verifyCommand(cmd); err == nil {
		t.Fatal("篡改指令类型后验签应失败")
	}
}

// TestVerifyCommandLegacySignature 新 Agent 必须兼容只签 ID 的旧服务端。
func TestVerifyCommandLegacySignature(t *testing.T) {
	secret := "test-secret"
	cmd := protocol.Command{
		ID:        "1",
		Type:      protocol.CmdUpgradeComponent,
		Payload:   protocol.CommandPayload{ToVersion: "2.0.0"},
		Timestamp: time.Now().Unix(),
		Nonce:     "nonce-legacy",
		ExpireAt:  time.Now().Add(5 * time.Minute).Truncate(time.Second),
	}
	body, _ := json.Marshal(cmd.Payload)
	cmd.Signature = signer.Sign(secret, signer.Envelope{
		Method: "CMD", Path: cmd.ID, Body: body,
		Timestamp: cmd.Timestamp, Nonce: cmd.Nonce,
	})

	a := New(Config{Secret: secret})
	if err := a.verifyCommand(cmd); err != nil {
		t.Fatalf("旧版 ID 签名应通过: %v", err)
	}
}

// TestVerifyCommandV2TakesPrecedence 新版签名存在时不得回退到旧签名，
// 防止 signature_v2 校验失败后改用旧签名实施降级。
func TestVerifyCommandV2TakesPrecedence(t *testing.T) {
	secret := "test-secret"
	cmd := protocol.Command{
		ID:        "1",
		Type:      protocol.CmdUpgradeComponent,
		Payload:   protocol.CommandPayload{ToVersion: "2.0.0"},
		Timestamp: time.Now().Unix(),
		Nonce:     "nonce-v2",
		ExpireAt:  time.Now().Add(5 * time.Minute).Truncate(time.Second),
	}
	body, _ := json.Marshal(cmd.Payload)
	env := signer.Envelope{
		Method: "CMD", Body: body,
		Timestamp: cmd.Timestamp, Nonce: cmd.Nonce,
	}
	env.Path = cmd.ID
	cmd.Signature = signer.Sign(secret, env) // 旧签名仍有效
	env.Path = cmd.SignedPath()
	cmd.SignatureV2 = signer.Sign(secret, env)

	a := New(Config{Secret: secret})
	if err := a.verifyCommand(cmd); err != nil {
		t.Fatalf("双签名应通过: %v", err)
	}

	// 类型变了：旧签名只签 ID 仍然“有效”，但 v2 必须拒绝。
	cmd.Type = protocol.CmdSelfUpdate
	if err := a.verifyCommand(cmd); err == nil {
		t.Fatal("signature_v2 存在时，篡改类型不得回退到旧签名")
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
