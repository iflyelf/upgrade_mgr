//go:build !windows

package watchdog

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentpath"
)

// lockDir 保活锁所在目录：必须仅 root 可写，不可使用 /var/lock 或 /tmp。
var lockDir = agentpath.LockDir()

// acquireLock 用 flock 获取非阻塞排他锁。
//
// 安全要点：绝不能把锁放在 /var/lock、/tmp 等 world-writable 目录。
// 实测 /run/lock 为 drwxrwxrwt，普通用户可预先创建并长期持有该锁，
// 使本函数每次都判定「另一实例在运行」而直接返回，
// 从而永久禁用 root Agent 的保活与崩溃回滚（拒绝服务 + 掩盖故障）。
// 因此锁文件放在仅 root 可写的运行目录，并用 O_NOFOLLOW 阻断符号链接替换。
func acquireLock() (release func(), ok bool, err error) {
	lockPath, err := ensureLockPath()
	if err != nil {
		return nil, false, err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, false, fmt.Errorf("打开锁文件失败（疑似被替换为符号链接）: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// 另一实例在运行
		_ = lock.Close()
		return nil, false, nil
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, true, nil
}

// detachProcess 让子进程脱离当前会话，避免 Agent 随 watchdog 退出。
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

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
