//go:build !windows

package installer

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/iflyelf/upgrade_mgr/internal/agent/osinfo"
)

// installPlatform 按 init 类型写服务并启动。
func installPlatform(opts Options, info osinfo.Info) error {
	switch info.Init {
	case "systemd":
		return installSystemd(opts)
	case "upstart":
		return installUpstart(opts)
	default:
		fmt.Printf("⚠️ 未检测到 systemd/upstart，跳过服务安装（可用 cron 看门狗或裸跑）\n")
		return nil
	}
}

func installSystemd(opts Options) error {
	content, err := render("upgrade-agent.service.tmpl", opts)
	if err != nil {
		return err
	}
	path := "/etc/systemd/system/upgrade-agent.service"
	if err := os.WriteFile(path, content, 0644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "upgrade-agent"); err != nil {
		return err
	}
	// 连续失败会触发 systemd start-limit，先清掉失败计数，保证重复安装能拉起。
	_ = run("systemctl", "reset-failed", "upgrade-agent")
	// 服务可能已在运行（例如重装/修复），必须显式 restart 才会加载新二进制和新 state。
	return run("systemctl", "restart", "upgrade-agent")
}

func installUpstart(opts Options) error {
	content, err := render("upgrade-agent.conf.tmpl", opts)
	if err != nil {
		return err
	}
	path := "/etc/init/upgrade-agent.conf"
	if err := os.WriteFile(path, content, 0644); err != nil {
		return err
	}
	// upstart 的 start 对已运行实例是空操作，先 stop 确保加载新二进制和新 state。
	_ = run("stop", "upgrade-agent")
	return run("start", "upgrade-agent")
}

// installWatchdog 安装 cron 看门狗（每分钟检查心跳并拉起）。
func installWatchdog(binPath, statePath string) error {
	line := fmt.Sprintf("* * * * * root %s watchdog --state %s --bin %s\n",
		binPath, statePath, binPath)
	return os.WriteFile("/etc/cron.d/upgrade-agent", []byte(line), 0644)
}

func removeWatchdog() {
	_ = os.Remove("/etc/cron.d/upgrade-agent")
}

// uninstallPlatform 停止并删除 Linux 服务。
func uninstallPlatform(info osinfo.Info) error {
	switch info.Init {
	case "systemd":
		_ = run("systemctl", "disable", "--now", "upgrade-agent")
		_ = os.Remove("/etc/systemd/system/upgrade-agent.service")
		_ = run("systemctl", "daemon-reload")
	case "upstart":
		_ = run("stop", "upgrade-agent")
		_ = os.Remove("/etc/init/upgrade-agent.conf")
	}
	return nil
}

// hardenStateDir 收紧 state 目录权限为 0700。
//
// 相对路径（dir="."）或根目录不做 chmod，避免误改当前目录/系统根目录。
func hardenStateDir(dir string) {
	if dir == "." || dir == string(filepath.Separator) {
		return
	}
	_ = os.Chmod(dir, 0700)
}
