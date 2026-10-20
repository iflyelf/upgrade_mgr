// Package svcmgr 服务管理抽象：systemd / sysv / upstart / none。
package svcmgr

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/runner"
)

// Manager 服务管理器接口
type Manager interface {
	Type() string
	Stop(name string) error
	Start(name string) error
	Restart(name string) error
	Status(name string) (string, error)
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
func (m *upstartMgr) Stop(name string) error {
	return runCmd("stop", name)
}
func (m *upstartMgr) Start(name string) error {
	return runCmd("start", name)
}
func (m *upstartMgr) Restart(name string) error {
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

func (m *sysvMgr) Type() string { return "sysv" }
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
func (m *noneMgr) Stop(string) error             { return fmt.Errorf("no init system") }
func (m *noneMgr) Start(string) error            { return fmt.Errorf("no init system") }
func (m *noneMgr) Restart(string) error          { return fmt.Errorf("no init system") }
func (m *noneMgr) Status(string) (string, error) { return "unknown", nil }

// ---- 辅助 ----

func runCmd(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := runner.Run(ctx, runner.Spec{Argv: args})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, res.Stderr)
	}
	return nil
}

// WriteSystemdUnit 写入 systemd unit 文件（install 时用）
func WriteSystemdUnit(name, content string) error {
	path := "/etc/systemd/system/" + name + ".service"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return err
	}
	return runCmd("systemctl", "daemon-reload")
}

// WriteUpstartConf 写入 upstart conf（CentOS6）
func WriteUpstartConf(name, content string) error {
	path := "/etc/init/" + name + ".conf"
	return os.WriteFile(path, []byte(content), 0644)
}
