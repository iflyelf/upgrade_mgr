//go:build windows

package installer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// prepareInstall 在复制二进制前停止旧服务、看门狗和残留 Agent 进程。
//
// Windows 不允许覆盖正在运行的 exe；即使改名，如果服务没有真正停止或
// 看门狗恰好拉起新进程，rename 仍会返回 Access denied。
func prepareInstall(opts Options) error {
	// 1. 先禁用看门狗计划任务，避免安装过程中被拉起再次占用 exe。
	//    安装结束后 Install 的 defer 会重新创建/启用（或按 --cron=false 删除）。
	disableWatchdog()

	// 2. 停止并等待旧服务退出。
	mg, err := mgr.Connect()
	if err == nil {
		if s, openErr := mg.OpenService(ServiceName); openErr == nil {
			st, qerr := s.Query()
			if qerr == nil && st.State != svc.Stopped {
				_, _ = s.Control(svc.Stop)
				if waitErr := waitStopped(s, 30*time.Second); waitErr != nil {
					// 服务未能正常停止：先关闭 SCM 自动恢复，避免 taskkill 后
					// SCM 5 秒后又把旧服务拉起来，与安装过程抢 exe。
					_ = s.SetRecoveryActionsOnNonCrashFailures(false)
					_ = s.ResetRecoveryActions()
				}
			}
			_ = s.Close()
		}
		_ = mg.Disconnect()
	}

	// 3. 清理仍以 upgrade-agent 镜像运行的残留进程（看门狗/手工 run/未退出的服务）。
	//    排除当前安装进程，避免 taskkill 把自己杀掉。
	killAgentProcesses(opts.BinPath)
	return nil
}

// disableWatchdog 禁用看门狗计划任务（任务不存在时忽略错误）。
func disableWatchdog() {
	_ = exec.Command("schtasks.exe", "/Change", "/TN", "upgrade-agent-watchdog", "/DISABLE").Run()
}

// killAgentProcesses 按镜像名结束残留 Agent 进程，排除当前进程。
func killAgentProcesses(binPath string) {
	name := filepath.Base(binPath)
	if name == "" || name == "." {
		name = "upgrade-agent.exe"
	}
	_ = exec.Command("taskkill.exe", "/F",
		"/FI", fmt.Sprintf("PID ne %d", os.Getpid()),
		"/IM", name).Run()
}
