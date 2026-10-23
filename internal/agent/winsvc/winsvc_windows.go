//go:build windows

// Package winsvc 把 Agent 包装成标准 Windows 服务（SCM）。
//
// 服务启动时由 SCM 调用 StartServiceCtrlDispatcher；若当前进程不是由 SCM
// 启动（例如运维在控制台手工执行），RunIfService 返回 false，调用方按普通
// 前台进程运行。
package winsvc

import (
	"context"
	"log"
	"time"

	"golang.org/x/sys/windows/svc"
)

// RunIfService 检测是否由 SCM 启动；是则接管为服务并阻塞直到服务停止。
//
// 返回 true 表示已按服务模式运行结束；false 表示不是服务，调用方继续前台运行。
func RunIfService(name string, run func(ctx context.Context) error) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, nil
	}

	h := &handler{run: run}
	// svc.Run 内部调用 StartServiceCtrlDispatcher，只有在 SCM 启动时才会成功。
	err = svc.Run(name, h)
	return true, err
}

type handler struct {
	run func(ctx context.Context) error
}

// Execute 由 SCM 调用；必须尽快返回并持续上报状态。
func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- h.run(ctx)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case err := <-done:
			status <- svc.Status{State: svc.StopPending}
			if err != nil {
				log.Printf("Agent 服务退出: %v", err)
				return true, 1
			}
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				// 给 Agent 最多 20 秒收尾；到点仍未退出则由 SCM 强制结束。
				select {
				case <-done:
				case <-time.After(20 * time.Second):
				}
				return false, 0
			}
		}
	}
}
