//go:build windows

package watchdog

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentpath"
)

// lockDir 看门狗锁目录（ProgramData 下，默认 ACL 仅管理员/SYSTEM 可写）。
var lockDir = agentpath.LockDir()

// acquireLock 用独占打开的锁文件实现单实例保护。
//
// Windows 没有 flock；以共享模式 0 打开锁文件，其他进程再次打开会得到
// ERROR_SHARING_VIOLATION，效果等同于非阻塞排他锁。句柄关闭即释放。
func acquireLock() (release func(), ok bool, err error) {
	lockPath, err := ensureLockPath()
	if err != nil {
		return nil, false, err
	}
	p, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateFile(p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, // 不共享：独占
		nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if err == windows.ERROR_SHARING_VIOLATION {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("打开看门狗锁文件失败: %w", err)
	}
	return func() { _ = windows.CloseHandle(h) }, true, nil
}

// detachProcess 以独立进程组启动，不继承控制台。
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// ensureLockPath 确保锁目录存在并返回锁文件路径。
func ensureLockPath() (string, error) {
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return "", fmt.Errorf("创建锁目录失败: %w", err)
	}
	return filepath.Join(lockDir, "watchdog.lock"), nil
}
