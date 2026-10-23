//go:build windows

package installer

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/iflyelf/upgrade_mgr/internal/agent/osinfo"
)

// installPlatform 注册 Windows 服务（SCM）并启动。
//
// 服务以 LocalSystem 运行（空 ServiceStartName 即 LocalSystem），
// 这样安装脚本、启停组件服务都不需要额外提权。
func installPlatform(opts Options, info osinfo.Info) error {
	mg, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器失败（请以管理员身份运行）: %w", err)
	}
	defer mg.Disconnect()

	// 重复安装：先停掉旧服务，再删除重建，确保 BinaryPathName 与参数更新。
	if s, err := mg.OpenService(ServiceName); err == nil {
		_, _ = s.Control(svc.Stop)
		_ = waitStopped(s, 20*time.Second)
		_ = s.Delete()
		_ = s.Close()
		// 服务删除后句柄释放可能有延迟，稍等避免 CreateService 报“已标记为删除”。
		time.Sleep(500 * time.Millisecond)
	}

	cfg := mgr.Config{
		DisplayName: "Upgrade Agent (upgrade_mgr)",
		Description: "upgrade_mgr 被控端：注册、心跳、长轮询执行升级指令",
		StartType:   mgr.StartAutomatic,
		ServiceType: windows.SERVICE_WIN32_OWN_PROCESS,
	}
	// 可选低权限服务账户；留空时 SCM 默认 LocalSystem。
	if acct := strings.TrimSpace(opts.ServiceAccount); acct != "" {
		cfg.ServiceStartName = acct
		cfg.Password = opts.ServicePassword
	}
	// 服务刚删除时句柄可能仍标记为待删除，CreateService 会返回
	// ERROR_SERVICE_MARKED_FOR_DELETE；短暂重试即可。
	var s *mgr.Service
	for i := 0; i < 10; i++ {
		s, err = mg.CreateService(ServiceName, opts.BinPath, cfg,
			"run", "--server", opts.ServerURL, "--state", opts.StatePath, "--service")
		if err == nil {
			break
		}
		if err != windows.ERROR_SERVICE_MARKED_FOR_DELETE {
			return fmt.Errorf("创建 Windows 服务失败: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("创建 Windows 服务失败（旧服务仍待删除，请稍后重试）: %w", err)
	}
	defer s.Close()

	// 失败自动重启：SCM 默认只在崩溃时重启，配合 Agent 自升级 exit(75)
	// 需要同时启用非崩溃失败重启。
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	_ = s.SetRecoveryActions(actions, 86400)
	_ = s.SetRecoveryActionsOnNonCrashFailures(true)

	if err := s.Start(); err != nil && err != windows.ERROR_SERVICE_ALREADY_RUNNING {
		return fmt.Errorf("启动 Windows 服务失败: %w", err)
	}
	return nil
}

func waitStopped(s *mgr.Service, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

// installWatchdog 注册每分钟运行的计划任务，作为 SCM 之外的兜底。
//
// 使用 schtasks（Server 2008 R2 起可用），SYSTEM 权限、隐藏窗口。
func installWatchdog(binPath, statePath string) error {
	tr := fmt.Sprintf(`"%s" watchdog --state "%s" --bin "%s"`, binPath, statePath, binPath)
	// /F 覆盖已存在任务；/RU SYSTEM 以系统权限运行。
	cmd := exec.Command("schtasks.exe", "/Create", "/TN", "upgrade-agent-watchdog",
		"/TR", tr, "/SC", "MINUTE", "/MO", "1", "/RU", "SYSTEM", "/RL", "HIGHEST", "/F")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removeWatchdog() {
	_ = exec.Command("schtasks.exe", "/Delete", "/TN", "upgrade-agent-watchdog", "/F").Run()
}

// uninstallPlatform 停止并删除 Windows 服务。
func uninstallPlatform(info osinfo.Info) error {
	mg, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器失败（请以管理员身份运行）: %w", err)
	}
	defer mg.Disconnect()
	s, err := mg.OpenService(ServiceName)
	if err != nil {
		return nil // 未安装
	}
	defer s.Close()
	_, _ = s.Control(svc.Stop)
	_ = waitStopped(s, 20*time.Second)
	if err := s.Delete(); err != nil && err != windows.ERROR_SERVICE_MARKED_FOR_DELETE {
		return fmt.Errorf("删除 Windows 服务失败: %w", err)
	}
	return nil
}

// hardenStateDir Windows 上 ProgramData 的默认 ACL 已限制为管理员/SYSTEM，
// os.Chmod 只影响只读属性，这里不做额外处理。
func hardenStateDir(dir string) {}
