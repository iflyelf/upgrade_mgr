// Package watchdog Agent 保活看门狗：单实例锁+心跳新鲜度+拉起。
//
// Linux 由 cron 每分钟调用；Windows 由计划任务每分钟调用（安装器注册）。
package watchdog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentpath"
	"github.com/iflyelf/upgrade_mgr/internal/agent/selfupdate"
)

// stateFile 是 watchdog 关心的 state 字段。
type stateFile struct {
	ServerURL     string
	AgentID       string
	LastHeartbeat time.Time `json:"last_heartbeat"`
	CrashCount    int       `json:"crash_count"`
}

// readState 严格读取 state：空文件/损坏文件必须报错，不能当作「心跳超时」。
//
// 否则 watchdog 会在 3 分钟后把正常但尚未注册的 Agent 判定为连续崩溃，
// 并触发 selfupdate.Rollback，造成误回滚。
func readState(path string) (stateFile, error) {
	var state stateFile
	data, err := os.ReadFile(path)
	if err != nil {
		return state, fmt.Errorf("state 文件不存在或无法读取，请先 install: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return state, fmt.Errorf("state 文件为空，请删除 %s 后重新执行 install --enroll-token", path)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("state 文件格式错误: %w", err)
	}
	return state, nil
}

// Check 检查 Agent 存活性，必要时拉起；连续崩溃触发回滚。
func Check(statePath, binPath string, threshold time.Duration) error {
	// 单实例保护：同一时刻只允许一个 watchdog 执行拉起/回滚。
	release, ok, err := acquireLock()
	if err != nil {
		return err
	}
	if !ok {
		// 另一实例在运行
		return nil
	}
	defer release()

	// 读 state 心跳
	state, err := readState(statePath)
	if err != nil {
		return err
	}
	// 尚未注册时没有心跳是正常的；不能计入崩溃，更不能触发回滚。
	if state.AgentID == "" {
		return fmt.Errorf("Agent 尚未注册，跳过看门狗（state 缺少 AgentID）")
	}

	// 心跳新鲜 → 存活，重置崩溃计数
	if time.Since(state.LastHeartbeat) < threshold {
		if state.CrashCount > 0 {
			state.CrashCount = 0
			updateState(statePath, state.CrashCount, state.LastHeartbeat)
		}
		return nil
	}

	// 超时 → 崩溃
	state.CrashCount++
	updateState(statePath, state.CrashCount, state.LastHeartbeat)

	// 连续崩溃 ≥3 次 → 回滚（防自升级后新版本持续崩溃）
	if state.CrashCount >= 3 {
		fmt.Printf("⚠️ Agent 连续崩溃 %d 次，触发回滚...\n", state.CrashCount)
		if err := selfupdate.Rollback(binPath, agentpath.BackupDir()); err != nil {
			return fmt.Errorf("回滚失败: %w", err)
		}
		state.CrashCount = 0
		updateState(statePath, state.CrashCount, state.LastHeartbeat)
		fmt.Println("✅ 已回滚到旧版本")
	}

	// 拉起
	fmt.Printf("Agent 心跳超时（上次 %s），拉起...\n", state.LastHeartbeat.Format(time.RFC3339))
	args := []string{"run", "--state", statePath}
	if state.ServerURL != "" {
		args = append(args, "--server", state.ServerURL)
	}
	if err := startAgent(binPath, args); err != nil {
		return fmt.Errorf("拉起失败: %w", err)
	}
	return nil
}

// updateState 只修改 crash_count / last_heartbeat，保留完整凭据字段。
func updateState(path string, crashCount int, lastHeartbeat time.Time) {
	existing, err := os.ReadFile(path)
	if err != nil {
		return // state 文件不存在，跳过
	}
	var fullState map[string]interface{}
	if err := json.Unmarshal(existing, &fullState); err != nil {
		return
	}
	fullState["crash_count"] = crashCount
	fullState["last_heartbeat"] = lastHeartbeat

	// 原子写回：tmp + fsync + rename
	b, _ := json.MarshalIndent(fullState, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return
	}
	if f, err := os.OpenFile(tmp, os.O_RDWR, 0600); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	_ = os.Rename(tmp, path)
}

// spawnAgent 直接拉起一个脱离当前会话的 Agent 进程（非 Windows 服务场景）。
func spawnAgent(binPath string, args []string) error {
	cmd := exec.Command(binPath, args...)
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
