//go:build !windows

package svcmgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/runner"
)

// 以下目录声明为变量，便于测试替换。
var (
	sysvInitDirs   = []string{"/etc/init.d", "/etc/rc.d/init.d"}
	upstartConfDir = "/etc/init"
)

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func sysvScriptExists(name string) bool {
	if !validName(name) {
		return false
	}
	for _, d := range sysvInitDirs {
		if fileExists(filepath.Join(d, name)) {
			return true
		}
	}
	return false
}

func upstartJobExists(name string) bool {
	return validName(name) && fileExists(filepath.Join(upstartConfDir, name+".conf"))
}

// parseLoadState 解析 `systemctl show -p LoadState` 输出，not-found 视为未注册。
func parseLoadState(out string) (state string, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "LoadState=") {
			return strings.TrimPrefix(line, "LoadState="), true
		}
	}
	return "", false
}

// New 根据 init 类型创建管理器
func New(initType string) Manager {
	switch initType {
	case "systemd":
		return &systemdMgr{}
	case "upstart":
		return &upstartMgr{}
	case "sysv":
		return &sysvMgr{}
	default:
		return &noneMgr{}
	}
}

// ---- systemd ----

type systemdMgr struct{}

func (m *systemdMgr) Type() string { return "systemd" }

// systemdUnitDirs unit 文件可能所在目录，用于识别「已写入但未 daemon-reload」的 unit。
var systemdUnitDirs = []string{
	"/etc/systemd/system", "/run/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system",
}

func systemdUnitFileExists(name string) bool {
	unit := name
	if !strings.Contains(unit, ".") {
		unit += ".service"
	}
	for _, d := range systemdUnitDirs {
		if fileExists(filepath.Join(d, unit)) {
			return true
		}
	}
	return false
}

// Exists 用 `systemctl show -p LoadState`（兼容 CentOS7 的老 systemd，不依赖 --value）。
// 无法判定时返回 true，保持原有行为让真实错误暴露出来。
// 若 systemd 报 not-found 但磁盘上已有 unit 文件（安装脚本刚写入、未 reload），
// 先 daemon-reload 再视为存在，保证启服阶段能找到新装的服务。
func (m *systemdMgr) Exists(name string) bool {
	if !validName(name) {
		return false
	}
	res, err := runner.Run(context.Background(), runner.Spec{
		Argv: []string{"systemctl", "show", "-p", "LoadState", "--", name}, Timeout: 5 * time.Second, LangC: true,
	})
	if err != nil || res.ExitCode != 0 {
		return true
	}
	state, ok := parseLoadState(res.Stdout)
	if !ok || state != "not-found" {
		return true
	}
	if systemdUnitFileExists(name) {
		_ = runCmd("systemctl", "daemon-reload")
		return true
	}
	return false
}

func (m *systemdMgr) Stop(name string) error {
	return runCmd("systemctl", "stop", name)
}
func (m *systemdMgr) Start(name string) error {
	return runCmd("systemctl", "start", name)
}
func (m *systemdMgr) Restart(name string) error {
	return runCmd("systemctl", "restart", name)
}
func (m *systemdMgr) Status(name string) (string, error) {
	res, _ := runner.Run(context.Background(), runner.Spec{
		Argv: []string{"systemctl", "is-active", name}, Timeout: 5 * time.Second,
	})
	return res.Stdout, nil
}

// ---- upstart (CentOS6) ----

type upstartMgr struct{}

func (m *upstartMgr) Type() string { return "upstart" }

// Exists CentOS6 上多数服务仍是 /etc/init.d 脚本，两者任一存在即视为已注册。
func (m *upstartMgr) Exists(name string) bool {
	return upstartJobExists(name) || sysvScriptExists(name)
}

// useSysv 没有 upstart job 但有 init.d 脚本时，改走 service 命令。
func (m *upstartMgr) useSysv(name string) bool {
	return !upstartJobExists(name) && sysvScriptExists(name)
}

func (m *upstartMgr) Stop(name string) error {
	if m.useSysv(name) {
		return runCmd("service", name, "stop")
	}
	return runCmd("stop", name)
}
func (m *upstartMgr) Start(name string) error {
	if m.useSysv(name) {
		return runCmd("service", name, "start")
	}
	return runCmd("start", name)
}
func (m *upstartMgr) Restart(name string) error {
	if m.useSysv(name) {
		return runCmd("service", name, "restart")
	}
	return runCmd("restart", name)
}
func (m *upstartMgr) Status(name string) (string, error) {
	res, _ := runner.Run(context.Background(), runner.Spec{
		Argv: []string{"status", name}, Timeout: 5 * time.Second,
	})
	return res.Stdout, nil
}

// ---- sysv ----

type sysvMgr struct{}

func (m *sysvMgr) Type() string            { return "sysv" }
func (m *sysvMgr) Exists(name string) bool { return sysvScriptExists(name) }
func (m *sysvMgr) Stop(name string) error {
	return runCmd("service", name, "stop")
}
func (m *sysvMgr) Start(name string) error {
	return runCmd("service", name, "start")
}
func (m *sysvMgr) Restart(name string) error {
	return runCmd("service", name, "restart")
}
func (m *sysvMgr) Status(name string) (string, error) {
	res, _ := runner.Run(context.Background(), runner.Spec{
		Argv: []string{"service", name, "status"}, Timeout: 5 * time.Second,
	})
	return res.Stdout, nil
}

// ---- none (裸跑) ----

type noneMgr struct{}

func (m *noneMgr) Type() string                  { return "none" }
func (m *noneMgr) Exists(string) bool            { return false }
func (m *noneMgr) Stop(string) error             { return fmt.Errorf("no init system") }
func (m *noneMgr) Start(string) error            { return fmt.Errorf("no init system") }
func (m *noneMgr) Restart(string) error          { return fmt.Errorf("no init system") }
func (m *noneMgr) Status(string) (string, error) { return "unknown", nil }

// ---- 辅助 ----

func writeSystemdUnit(name, content string) error {
	path := "/etc/systemd/system/" + name + ".service"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return err
	}
	return runCmd("systemctl", "daemon-reload")
}

func writeUpstartConf(name, content string) error {
	path := "/etc/init/" + name + ".conf"
	return os.WriteFile(path, []byte(content), 0644)
}
