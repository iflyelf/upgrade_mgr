package watchdog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRunLockExclusive 同一主机只能有一个 Agent 持有运行锁。
func TestRunLockExclusive(t *testing.T) {
	old := lockDir
	lockDir = t.TempDir()
	defer func() { lockDir = old }()

	release, ok, err := AcquireRunLock()
	if err != nil || !ok {
		t.Fatalf("首次获取运行锁失败: ok=%v err=%v", ok, err)
	}
	if !agentRunning() {
		t.Fatal("持有运行锁时应判定 Agent 存活")
	}
	if _, ok2, _ := AcquireRunLock(); ok2 {
		t.Fatal("第二个实例不应拿到运行锁")
	}
	release()
	if agentRunning() {
		t.Fatal("释放运行锁后不应判定 Agent 存活")
	}
}

// TestCheckSkipsSpawnWhenAgentAlive 心跳超时但进程仍持有运行锁（如自升级下载中）时，
// 看门狗不得计入崩溃，也不得再拉起第二个进程。
func TestCheckSkipsSpawnWhenAgentAlive(t *testing.T) {
	old := lockDir
	lockDir = t.TempDir()
	defer func() { lockDir = old }()

	statePath := filepath.Join(t.TempDir(), "state.json")
	stale := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	content := `{"ServerURL":"http://mgr","AgentID":"agent-1","Token":"t","Secret":"s","last_heartbeat":"` + stale + `","crash_count":2}`
	if err := os.WriteFile(statePath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	release, ok, err := AcquireRunLock()
	if err != nil || !ok {
		t.Fatalf("获取运行锁失败: ok=%v err=%v", ok, err)
	}
	defer release()

	// binPath 不存在：若误拉起会返回「拉起失败」。
	if err := Check(statePath, filepath.Join(t.TempDir(), "missing-agent"), 3*time.Minute); err != nil {
		t.Fatalf("Agent 存活时 Check 不应拉起/报错: %v", err)
	}
	st, err := readState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if st.CrashCount != 0 {
		t.Fatalf("Agent 存活时崩溃计数应清零, got %d", st.CrashCount)
	}
}
