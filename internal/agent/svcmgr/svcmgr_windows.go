//go:build windows

package svcmgr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/runner"
)

// New 根据 init 类型创建管理器。Windows 统一使用 SCM。
func New(initType string) Manager {
	return &windowsMgr{}
}

// windowsMgr 通过 Windows 服务控制管理器（SCM）管理服务。
//
// 使用 x/sys/windows/svc/mgr 而非 sc.exe：服务名可含空格，
// 且能准确拿到 STOPPED/RUNNING 状态，避免英文/中文系统输出差异。
type windowsMgr struct{}

func (m *windowsMgr) Type() string { return "windows" }

func (m *windowsMgr) Exists(name string) bool {
	if !validName(name) {
		return false
	}
	mg, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer mg.Disconnect()
	s, err := mg.OpenService(name)
	if err != nil {
		return false
	}
	_ = s.Close()
	return true
}

func (m *windowsMgr) Stop(name string) error {
	return m.control(name, false, svc.Stopped)
}

func (m *windowsMgr) Start(name string) error {
	return m.control(name, true, svc.Running)
}

func (m *windowsMgr) Restart(name string) error {
	if err := m.Stop(name); err != nil {
		if !m.Exists(name) {
			// 首次安装：服务尚未注册，跳过。
			return nil
		}
		return err
	}
	return m.Start(name)
}

func (m *windowsMgr) Status(name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("非法服务名 %q", name)
	}
	mg, err := mgr.Connect()
	if err != nil {
		return "", err
	}
	defer mg.Disconnect()
	s, err := mg.OpenService(name)
	if err != nil {
		return "not-found", nil
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "", err
	}
	return serviceStateString(st.State), nil
}

// control 启动或停止服务并等待其到达目标状态。
//
// x/sys/windows/svc 没有提供 svc.Start 控制码，启动统一走 Service.Start()。
func (m *windowsMgr) control(name string, start bool, want svc.State) error {
	if !validName(name) {
		return fmt.Errorf("非法服务名 %q", name)
	}
	mg, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer mg.Disconnect()
	s, err := mg.OpenService(name)
	if err != nil {
		return fmt.Errorf("打开服务 %s 失败: %w", name, err)
	}
	defer s.Close()

	if start {
		if err := s.Start(); err != nil && err != windows.ERROR_SERVICE_ALREADY_RUNNING {
			return fmt.Errorf("启动服务 %s 失败: %w", name, err)
		}
	} else {
		if _, err := s.Control(svc.Stop); err != nil && err != windows.ERROR_SERVICE_NOT_ACTIVE {
			return fmt.Errorf("停止服务 %s 失败: %w", name, err)
		}
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待服务 %s 到达状态 %d 超时（当前 %d）", name, want, st.State)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func serviceStateString(st svc.State) string {
	switch st {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start-pending"
	case svc.StopPending:
		return "stop-pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue-pending"
	case svc.PausePending:
		return "pause-pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("state-%d", st)
	}
}

// writeSystemdUnit / writeUpstartConf 在 Windows 上不使用；保留空实现以兼容调用方。
func writeSystemdUnit(name, content string) error {
	return fmt.Errorf("Windows 不支持 systemd unit")
}

func writeUpstartConf(name, content string) error {
	return fmt.Errorf("Windows 不支持 upstart")
}

// windowsServiceQuery 通过 sc.exe 查询服务，作为 SCM API 失败时的兜底。
func windowsServiceQuery(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := runner.Run(ctx, runner.Spec{Argv: []string{"sc.exe", "query", name}, Timeout: 5 * time.Second})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}
