// Package runner 统一的命令执行器：直接 exec（program + argv），不经过 /bin/sh。
//
// 设计要点（升级平台的安全底线）：
//   - 不使用 shell，杜绝命令注入与管道/重定向带来的不确定性；
//   - 版本探测、健康检查、pre/post 钩子、exec 部署模式共用本执行器；
//   - 健康检查以「退出码 == 0」为准，不解析 stdout 管道结果；
//   - 支持超时、工作目录、附加环境变量、以指定用户运行（可选）。
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Spec 一次命令执行的描述。
type Spec struct {
	// Argv 程序与参数，Argv[0] 为可执行文件（绝对路径或 PATH 可寻址）。
	Argv []string
	// Dir 工作目录，空则继承当前进程。
	Dir string
	// Env 追加的环境变量（KEY=VALUE），会与当前进程环境合并。
	Env []string
	// Timeout 超时；<=0 表示不限制（不建议，升级场景务必设置）。
	Timeout time.Duration
	// LangC 为 true 时强制 LANG=C/LC_ALL=C，稳定命令输出（CentOS6 中文环境解析）。
	LangC bool
}

// Result 执行结果。
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
	TimedOut bool
}

// ErrEmptyArgv 空命令。
var ErrEmptyArgv = errors.New("runner: 空命令")

// Run 执行命令并返回结果。
//
// 返回的 error 仅表示「无法启动/超时」等执行框架层面的错误；
// 命令本身以非零退出码结束时 error 为 nil，退出码在 Result.ExitCode 中。
// 调用方据此判定成功（ExitCode==0）或失败。
func Run(ctx context.Context, spec Spec) (*Result, error) {
	if len(spec.Argv) == 0 || strings.TrimSpace(spec.Argv[0]) == "" {
		return nil, ErrEmptyArgv
	}

	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir

	// 环境：当前进程环境 + LangC + 附加 Env（后者优先，去重由 exec 内核处理，末项生效）
	env := currentEnv()
	if spec.LangC {
		env = append(env, "LANG=C", "LC_ALL=C")
	}
	env = append(env, spec.Env...)
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	res := &Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: dur,
	}

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, fmt.Errorf("命令超时（%s）: %s", spec.Timeout, strings.Join(spec.Argv, " "))
	}

	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// 命令以非零退出码结束：属于「正常执行完成」，退出码带回
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		// 无法启动（文件不存在/权限等）
		res.ExitCode = -1
		return res, fmt.Errorf("命令启动失败: %s: %w", strings.Join(spec.Argv, " "), err)
	}

	res.ExitCode = 0
	return res, nil
}

// Check 执行命令并返回是否成功（退出码 == 0），用于健康检查/版本命令。
func Check(ctx context.Context, spec Spec) bool {
	res, err := Run(ctx, spec)
	return err == nil && res.ExitCode == 0
}

// currentEnv 返回当前进程环境的副本（便于测试替换）。
var currentEnv = func() []string {
	return append([]string{}, osEnviron()...)
}
