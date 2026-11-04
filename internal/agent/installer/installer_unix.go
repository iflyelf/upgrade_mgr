//go:build !windows

package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// 旧版本曾用 logrotate 管理 /var/log/upgrade-agent.log；现在由 Agent 自切割，
	// 删除旧配置避免双重轮转。
	_ = os.Remove("/etc/logrotate.d/upgrade-agent")
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

// isServiceProcess 判断当前进程是否由 init/upstart/systemd 拉起。
//
// systemd/upstart 的服务进程父进程为 PID 1；手工前台 run 的父进程是 shell，
// 不应覆盖已安装服务配置。
func isServiceProcess() bool { return os.Getppid() == 1 }

// refreshPlatform 自升级时刷新已存在的服务配置，不重启服务。
//
// 只替换 ExecStart / exec 行与 respawn limit，保留服务文件中的其他自定义内容；
// 旧版 upstart 的 script + stderr 重定向会被替换为 Agent 自写日志的 exec 行。
func refreshPlatform(opts Options, info osinfo.Info) (bool, error) {
	switch info.Init {
	case "systemd":
		path := "/etc/systemd/system/upgrade-agent.service"
		old, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil // 未安装为服务（前台运行），无需刷新
			}
			return false, err
		}
		expected := "ExecStart=" + opts.BinPath + " " + serviceArgs(opts)
		updated, found := replaceServiceLine(string(old), "ExecStart=", expected)
		if !found {
			rendered, rerr := render("upgrade-agent.service.tmpl", opts)
			if rerr != nil {
				return false, rerr
			}
			updated = string(rendered)
		}
		if updated == string(old) {
			return false, nil
		}
		if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
			return true, err
		}
		return true, run("systemctl", "daemon-reload")
	case "upstart":
		path := "/etc/init/upgrade-agent.conf"
		old, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		expected := "exec " + opts.BinPath + " " + serviceArgs(opts)
		updated, found := updateUpstartConfig(string(old), expected)
		if !found {
			rendered, rerr := render("upgrade-agent.conf.tmpl", opts)
			if rerr != nil {
				return false, rerr
			}
			updated = string(rendered)
		}
		// 旧版本可能残留外部 logrotate 配置，统一清理，避免与 Agent 自切割冲突。
		_ = os.Remove("/etc/logrotate.d/upgrade-agent")
		if updated == string(old) {
			return false, nil
		}
		if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
			return true, err
		}
		return true, run("initctl", "reload-configuration")
	default:
		return false, nil
	}
}

// replaceServiceLine 替换首行以 prefix 开头的内容；未找到时返回 found=false。
func replaceServiceLine(content, prefix, replacement string) (string, bool) {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			lines[i] = replacement
			return strings.Join(lines, "\n"), true
		}
	}
	return content, false
}

// updateUpstartConfig 更新 upstart 的 exec/script 行与 respawn limit。
//
// 返回 found=true 表示找到可替换的启动命令；false 时调用方改用完整模板。
func updateUpstartConfig(content, execLine string) (string, bool) {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "respawn limit") && t != "respawn limit 10 5" {
			lines[i] = "respawn limit 10 5"
		}
	}
	// 旧中间版本：script / umask / exec >>... 2>&1 / end script
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "script" {
			start = i
			break
		}
	}
	if start >= 0 {
		end := -1
		for i := start + 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "end script" {
				end = i
				break
			}
		}
		if end > start {
			out := make([]string, 0, len(lines)-(end-start))
			out = append(out, lines[:start]...)
			out = append(out, execLine)
			out = append(out, lines[end+1:]...)
			return strings.Join(out, "\n"), true
		}
	}
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "exec ") {
			lines[i] = execLine
			return strings.Join(lines, "\n"), true
		}
	}
	return strings.Join(lines, "\n"), false
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
		_ = os.Remove("/etc/logrotate.d/upgrade-agent")
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
