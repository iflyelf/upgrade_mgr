//go:build windows

package installer

import (
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// prepareInstall 在复制二进制前停止已有服务。
//
// Windows 不允许覆盖正在运行的 exe；虽然改名可行，但先停服务能避免
// 「服务已被标记为删除」等瞬时状态，重装更稳定。
func prepareInstall(opts Options) {
	mg, err := mgr.Connect()
	if err != nil {
		return
	}
	defer mg.Disconnect()
	s, err := mg.OpenService(ServiceName)
	if err != nil {
		return
	}
	defer s.Close()
	st, err := s.Query()
	if err == nil && st.State != svc.Stopped {
		_, _ = s.Control(svc.Stop)
		waitStopped(s, 20*time.Second)
	}
}
