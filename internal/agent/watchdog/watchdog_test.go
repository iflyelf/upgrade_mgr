package watchdog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadStateRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := readState(path)
	if err == nil || !strings.Contains(err.Error(), "state 文件为空") {
		t.Fatalf("空 state 应返回错误，got %v", err)
	}
}

func TestReadStateRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := readState(path)
	if err == nil || !strings.Contains(err.Error(), "格式错误") {
		t.Fatalf("损坏 state 应返回错误，got %v", err)
	}
}

func TestReadStateUnregistered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"ServerURL":"https://mgr.example.com","EnrollToken":"tok"}`), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := readState(path)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if st.AgentID != "" {
		t.Fatalf("AgentID = %q, want empty", st.AgentID)
	}
}

func TestUpdateStatePreservesCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := `{"ServerURL":"https://mgr.example.com","AgentID":"agent-1","Token":"token-1","Secret":"secret-1"}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	hb := time.Now().Add(-time.Hour).Truncate(time.Second)
	updateState(path, 2, hb)

	var st struct {
		ServerURL, AgentID, Token, Secret string
		CrashCount                        int       `json:"crash_count"`
		LastHeartbeat                     time.Time `json:"last_heartbeat"`
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("解析 state: %v", err)
	}
	if st.AgentID != "agent-1" || st.Token != "token-1" || st.Secret != "secret-1" {
		t.Fatalf("凭据被覆盖: %+v", st)
	}
	if st.CrashCount != 2 || !st.LastHeartbeat.Equal(hb) {
		t.Fatalf("看门狗字段未更新: crash=%d hb=%v", st.CrashCount, st.LastHeartbeat)
	}
}
