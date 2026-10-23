// Package svcmgr 服务管理抽象：systemd / sysv / upstart / none / windows(SCM)。
package svcmgr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/runner"
)

// Manager 服务管理器接口
type Manager interface {
	Type() string
	// Exists 服务是否已在本机注册（unit / init 脚本 / SCM 服务存在）。
	// 首次安装时服务尚未注册，执行器据此跳过停服，避免误判失败。
	Exists(name string) bool
	Stop(name string) error
	Start(name string) error
	Restart(name string) error
	Status(name string) (string, error)
}

// validName 拒绝空名、路径分隔符与选项注入，防止拼接路径越界。
//
// Windows 服务名允许空格等字符，但平台侧的 sc.exe 调用使用参数数组，
// 不存在 shell 注入；这里仍拒绝路径分隔符与控制字符。
func validName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\\x00") && !strings.HasPrefix(name, "-") && name != "." && name != ".."
}

// runCmd 执行服务管理命令，非零退出码视为失败。
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

// WriteSystemdUnit 写入 systemd unit 文件（install 时用）。
func WriteSystemdUnit(name, content string) error {
	return writeSystemdUnit(name, content)
}

// WriteUpstartConf 写入 upstart conf（CentOS6）。
func WriteUpstartConf(name, content string) error {
	return writeUpstartConf(name, content)
}
