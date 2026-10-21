package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteInitialStateWritesServerAndToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeInitialState(path, "https://mgr.example.com", "tok-123"); err != nil {
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
	if err := writeInitialState(path, "https://mgr.example.com", "tok-456"); err != nil {
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

	if err := writeInitialState(path, "https://new.example.com", "new-token"); err != nil {
		t.Fatalf("writeInitialState: %v", err)
	}

	var st struct {
		ServerURL, AgentID, Token, Secret string
	}
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
