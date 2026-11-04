//go:build windows

package installer

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
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
		if err := waitStopped(s, 20*time.Second); err != nil {
			_ = s.Close()
			return fmt.Errorf("停止旧服务失败: %w", err)
		}
		_ = s.Delete()
		_ = s.Close()
	}
	// SCM 删除服务是异步的：必须等服务名真正消失，否则 CreateService 会返回
	// ERROR_SERVICE_MARKED_FOR_DELETE。最多等待 60 秒，期间继续尝试 stop/delete。
	if err := waitServiceGone(mg, 60*time.Second); err != nil {
		return fmt.Errorf("旧服务仍待删除，请稍后重试: %w", err)
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
	// ERROR_SERVICE_MARKED_FOR_DELETE；持续重试最多 60 秒。
	var s *mgr.Service
	for i := 0; i < 120; i++ {
		s, err = mg.CreateService(ServiceName, opts.BinPath, cfg,
			"run", "--server", opts.ServerURL, "--state", opts.StatePath,
			"--log", opts.LogPath, "--log-max-mb", strconv.Itoa(opts.LogMaxMB),
			"--log-backups", strconv.Itoa(opts.LogBackups), "--service")
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
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
	if err := ensureRecoveryActions(s); err != nil {
		return err
	}

	if err := s.Start(); err != nil && err != windows.ERROR_SERVICE_ALREADY_RUNNING {
		return fmt.Errorf("启动 Windows 服务失败: %w", err)
	}
	return nil
}

// ensureRecoveryActions 确保 SCM 在自升级 exit(75)/崩溃后自动重启服务。
func ensureRecoveryActions(s *mgr.Service) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, 86400); err != nil {
		return fmt.Errorf("设置服务恢复动作失败（自升级后可能无法自动重启）: %w", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("启用非崩溃失败恢复失败: %w", err)
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
	return fmt.Errorf("等待服务停止超时（%s）", d)
}

// waitServiceGone 等待服务名从 SCM 中真正消失。
//
// Delete 只是标记删除；若仍有进程/句柄持有服务，CreateService 会返回
// ERROR_SERVICE_MARKED_FOR_DELETE。这里持续尝试 stop/delete 直到 OpenService
// 返回“不存在”或超时。
func waitServiceGone(mg *mgr.Mgr, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		s, err := mg.OpenService(ServiceName)
		if err != nil {
			if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
				return nil
			}
			if !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
				return err
			}
		} else {
			_, _ = s.Control(svc.Stop)
			_ = s.Delete()
			_ = s.Close()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待服务删除超时（%s）", d)
		}
		time.Sleep(1 * time.Second)
	}
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

// isServiceProcess 判断当前进程是否由 Windows SCM 启动。
func isServiceProcess() bool {
	is, err := svc.IsWindowsService()
	return err == nil && is
}

// refreshPlatform 自升级时更新已存在 Windows 服务的启动参数，不重启服务。
func refreshPlatform(opts Options, info osinfo.Info) (bool, error) {
	m, err := mgr.Connect()
	if err != nil {
		return false, err
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return false, nil // 未安装为服务（前台运行），无需刷新
	}
	defer s.Close()

	cfg, err := s.Config()
	if err != nil {
		return false, err
	}
	cmdline := buildServiceCommandLine(opts)
	changed := false
	if cfg.BinaryPathName != cmdline {
		cfg.BinaryPathName = cmdline
		if err := s.UpdateConfig(cfg); err != nil {
			return false, err
		}
		changed = true
	}
	// 无论启动参数是否变化，都幂等确保 SCM 恢复动作存在：
	// 自升级 exit(75) 后由 SCM 5 秒后自动拉起，避免长时间离线。
	if err := ensureRecoveryActions(s); err != nil {
		return changed, err
	}
	return changed, nil
}

// buildServiceCommandLine 构造 SCM BINARY_PATH_NAME，保留原有服务账户等配置。
func buildServiceCommandLine(opts Options) string {
	args := []string{
		"run", "--server", opts.ServerURL, "--state", opts.StatePath,
		"--log", opts.LogPath, "--log-max-mb", strconv.Itoa(opts.LogMaxMB),
		"--log-backups", strconv.Itoa(opts.LogBackups), "--service",
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, syscall.EscapeArg(opts.BinPath))
	for _, a := range args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
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
