//go:build windows

package watchdog

import (
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/iflyelf/upgrade_mgr/internal/agent/installer"
)

// startAgent Windows 上通过 SCM 恢复 Agent。
//
// 直接拉起进程会脱离服务控制，自升级 exit(75) 后无人重启；
// 服务已存在时按服务状态处理：停止则启动，运行中（进程假死）则重启。
// 服务不存在（手工安装未注册服务）时才退化为拉起普通进程。
func startAgent(binPath string, args []string) error {
	if startService() == nil {
		return nil
	}
	return spawnAgent(binPath, args)
}

// startService 启动或重启 upgrade-agent 服务。
func startService() error {
	mg, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer mg.Disconnect()
	s, err := mg.OpenService(installer.ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()

	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Running {
		// 心跳已超时但服务仍是 RUNNING：进程可能假死，重启一次。
		if _, err := s.Control(svc.Stop); err != nil && err != windows.ERROR_SERVICE_NOT_ACTIVE {
			return err
		}
		if !waitState(s, svc.Stopped, 20*time.Second) {
			return nil
		}
	}
	if err := s.Start(); err != nil && err != windows.ERROR_SERVICE_ALREADY_RUNNING {
		return err
	}
	// 简单等待服务进入 RUNNING，避免紧接着的下一次 watchdog 重复拉起。
	waitState(s, svc.Running, 5*time.Second)
	return nil
}

// waitState 等待服务到达目标状态，超时返回 false。
func waitState(s *mgr.Service, want svc.State, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return false
		}
		if st.State == want {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}
