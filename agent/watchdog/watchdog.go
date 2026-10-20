// Package watchdog Agent 保活看门狗：flock 单实例+心跳新鲜度+拉起。
//
// 设计：由 cron 每分钟调用一次；检查 state 心跳新鲜度，超阈值则拉起 Agent。
package watchdog

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/agent/selfupdate"
)

// Check 检查 Agent 存活性，必要时拉起；连续崩溃触发回滚。
func Check(statePath, binPath string, threshold time.Duration) error {
	// flock 单实例保护。
	//
	// 安全要点：绝不能把锁放在 /var/lock、/tmp 等 world-writable 目录。
	// 实测 /run/lock 为 drwxrwxrwt，普通用户可预先创建并长期持有该锁，
	// 使本函数每次都判定「另一实例在运行」而直接返回，
	// 从而永久禁用 root Agent 的保活与崩溃回滚（拒绝服务 + 掩盖故障）。
	// 因此锁文件放在仅 root 可写的运行目录，并用 O_NOFOLLOW 阻断符号链接替换。
	lockPath, err := ensureLockPath()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return fmt.Errorf("打开锁文件失败（疑似被替换为符号链接）: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// 另一实例在运行
		return nil
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	// 读 state 心跳
	data, err := os.ReadFile(statePath)
	if err != nil {
		return fmt.Errorf("state 文件不存在或无法读取，请先 install")
	}
	var state struct {
		LastHeartbeat time.Time `json:"last_heartbeat"`
		CrashCount    int       `json:"crash_count"`
	}
	_ = json.Unmarshal(data, &state)

	// 心跳新鲜 → 存活，重置崩溃计数
	if time.Since(state.LastHeartbeat) < threshold {
		if state.CrashCount > 0 {
			state.CrashCount = 0
			updateState(statePath, state)
		}
		return nil
	}

	// 超时 → 崩溃
	state.CrashCount++
	updateState(statePath, state)

	// 连续崩溃 ≥3 次 → 回滚（防自升级后新版本持续崩溃）
	if state.CrashCount >= 3 {
		fmt.Printf("⚠️ Agent 连续崩溃 %d 次，触发回滚...\n", state.CrashCount)
		backupDir := "/var/lib/upgrade-agent/backup"
		if err := selfupdate.Rollback(binPath, backupDir); err != nil {
			return fmt.Errorf("回滚失败: %w", err)
		}
		state.CrashCount = 0
		updateState(statePath, state)
		fmt.Println("✅ 已回滚到旧版本")
	}

	// 拉起
	fmt.Printf("Agent 心跳超时（上次 %s），拉起...\n", state.LastHeartbeat.Format(time.RFC3339))
	cmd := exec.Command(binPath, "run", "--state", statePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("拉起失败: %w", err)
	}
	return cmd.Process.Release()
}

func updateState(path string, data any) {
	// 只更新崩溃计数，不覆盖完整 state 文件（避免丢失 AgentID/Token/Secret）
	// 读取完整 state
	existing, err := os.ReadFile(path)
	if err != nil {
		return // state 文件不存在，跳过
	}
	var fullState map[string]interface{}
	if err := json.Unmarshal(existing, &fullState); err != nil {
		return
	}
	
	// 只更新 crash_count 和 last_heartbeat 字段
	if partial, ok := data.(struct {
		LastHeartbeat time.Time `json:"last_heartbeat"`
		CrashCount    int       `json:"crash_count"`
	}); ok {
		fullState["crash_count"] = partial.CrashCount
		fullState["last_heartbeat"] = partial.LastHeartbeat
	}
	
	// 原子写回：tmp + fsync + rename
	b, _ := json.MarshalIndent(fullState, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return
	}
	if f, err := os.OpenFile(tmp, os.O_RDWR, 0600); err == nil {
		f.Sync()
		f.Close()
	}
	_ = os.Rename(tmp, path)
}

// lockDir 保活锁所在目录：必须仅 root 可写，不可使用 /var/lock 或 /tmp。
const lockDir = "/var/lib/upgrade-agent"

// ensureLockPath 确保锁目录存在且权限收紧（0700），返回锁文件绝对路径。
//
// 若目录已存在但权限过宽（组/其他可写），主动收紧，避免非特权用户抢占锁。
func ensureLockPath() (string, error) {
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return "", fmt.Errorf("创建锁目录失败: %w", err)
	}
	if fi, err := os.Stat(lockDir); err == nil {
		if fi.Mode().Perm()&0022 != 0 {
			if err := os.Chmod(lockDir, 0700); err != nil {
				return "", fmt.Errorf("锁目录权限过宽且无法收紧: %w", err)
			}
		}
	}
	return filepath.Join(lockDir, "watchdog.lock"), nil
}
