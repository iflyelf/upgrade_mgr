package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/runner"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// CommandResult 一次性命令执行结果。
type CommandResult struct {
	Success  bool
	ExitCode int
	TimedOut bool
	Log      string
}

// ExecuteCommand 执行一次性 Shell 脚本（run_command 指令）。
//
// 与组件升级流水线完全独立：不下载制品、不备份、不停服、不回滚，
// 只把脚本写入临时文件后交给解释器执行。
//
// 交互脚本支持：
//   - Stdin：把内容作为标准输入（如 "Y\n"），等价于 here-string；
//   - TTY：通过 script(1) 分配伪终端，兼容直接读写 /dev/tty 的脚本
//     （例如卸载脚本用 `dd if=/dev/tty` 读取确认）。
func ExecuteCommand(ctx context.Context, spec protocol.CommandSpec, onOutput func(string), onStage func(stage, message string)) *CommandResult {
	res := &CommandResult{ExitCode: -1}
	logf := func(stage, msg string, args ...any) {
		if onStage != nil {
			onStage(stage, fmt.Sprintf(msg, args...))
		}
	}

	script := strings.TrimSpace(spec.Script)
	if script == "" {
		res.Log = "[exec] 脚本内容为空\n"
		return res
	}
	timeout, _ := time.ParseDuration(strings.TrimSpace(spec.Timeout))
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	dir := os.TempDir()
	if d := strings.TrimSpace(spec.WorkDir); d != "" {
		dir = d
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		res.Log = fmt.Sprintf("[exec] 创建工作目录失败: %v\n", err)
		return res
	}
	f, err := os.CreateTemp(dir, "run-command-*.sh")
	if err != nil {
		res.Log = fmt.Sprintf("[exec] 创建临时脚本失败: %v\n", err)
		return res
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.WriteString(script + "\n"); err != nil {
		f.Close()
		res.Log = fmt.Sprintf("[exec] 写入临时脚本失败: %v\n", err)
		return res
	}
	if err := f.Close(); err != nil {
		res.Log = fmt.Sprintf("[exec] 写入临时脚本失败: %v\n", err)
		return res
	}
	if err := os.Chmod(path, 0700); err != nil {
		res.Log = fmt.Sprintf("[exec] 设置脚本权限失败: %v\n", err)
		return res
	}

	interp := strings.TrimSpace(spec.Interpreter)
	if interp == "" {
		interp = shellPath()
	}
	// 只允许解释器白名单，避免服务端被攻破后通过 interpreter 执行任意程序。
	if err := validateInterpreter(interp); err != nil {
		res.Log = fmt.Sprintf("[exec] %v\n", err)
		return res
	}

	argv := append([]string{interp, path}, spec.Args...)
	if spec.TTY {
		argv = ttyCommand(interp, path, spec.Args)
	}

	logf(protocol.StageRunCommand, "执行命令（%s，超时 %s%s）", filepath.Base(interp), timeout,
		ttySuffix(spec.TTY, spec.Stdin))

	stream := &chunkWriter{onOutput: onOutput}
	out, err := runner.Run(ctx, runner.Spec{
		Argv: argv, Dir: strings.TrimSpace(spec.WorkDir), Env: spec.Env,
		Timeout: timeout, Stdin: spec.Stdin, Stream: stream,
	})
	// runner 已通过 Stream 实时送出；这里只把最后不足一个批次的内容补发。
	stream.Flush()

	if out != nil {
		res.ExitCode = out.ExitCode
		res.TimedOut = out.TimedOut
		res.Log = strings.TrimSpace(out.Stdout)
		if s := strings.TrimSpace(out.Stderr); s != "" {
			if res.Log != "" {
				res.Log += "\n"
			}
			res.Log += s
		}
		if res.Log != "" {
			res.Log += "\n"
		}
	}
	if err != nil {
		res.Log += fmt.Sprintf("[exec] %v\n", err)
		return res
	}
	if out.ExitCode != 0 && !spec.IgnoreError {
		res.Log += fmt.Sprintf("[exec] 命令退出码 %d\n", out.ExitCode)
		return res
	}
	res.Success = true
	return res
}

// validateInterpreter 限定解释器白名单（bash/sh），防止把任意二进制当成脚本执行器。
func validateInterpreter(p string) error {
	base := filepath.Base(strings.TrimSpace(p))
	switch base {
	case "bash", "sh", "dash", "ksh", "zsh":
		return nil
	default:
		return fmt.Errorf("不支持的解释器 %q（仅允许 bash/sh/dash/ksh/zsh）", p)
	}
}

// ttyCommand 构造通过 script(1) 启动的命令，把脚本输出接入伪终端。
//
// script -q -c "<cmd>" /dev/null：quiet、不额外记录 typescript 文件。
// 命令字符串中的每个参数单独做 shell 单引号转义，避免注入。
func ttyCommand(interp, path string, args []string) []string {
	parts := make([]string, 0, len(args)+2)
	parts = append(parts, shellQuote(interp), shellQuote(path))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	cmd := strings.Join(parts, " ")
	return []string{"script", "-q", "-c", cmd, "/dev/null"}
}

func ttySuffix(tty bool, stdin string) string {
	var out []string
	if tty {
		out = append(out, "伪终端")
	}
	if stdin != "" {
		out = append(out, fmt.Sprintf("stdin %d 字节", len(stdin)))
	}
	if len(out) == 0 {
		return ""
	}
	return "，" + strings.Join(out, "，")
}

// shellQuote 用单引号包裹参数，内部单引号按 POSIX 方式转义。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// chunkWriter 把脚本输出按块回调给上层（用于实时日志上报）。
//
// 块大小控制在 2KB 左右且按时间节流，避免大量输出时产生过多 HTTP 请求。
// stdout/stderr 由 os/exec 分别用独立 goroutine 复制，因此必须加锁。
type chunkWriter struct {
	mu       sync.Mutex
	onOutput func(string)
	buf      []byte
	last     time.Time
}

const chunkFlushBytes = 2048
const chunkFlushInterval = 800 * time.Millisecond

func (w *chunkWriter) Write(p []byte) (int, error) {
	if w.onOutput == nil {
		return len(p), nil
	}
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	needFlush := len(w.buf) >= chunkFlushBytes || time.Since(w.last) >= chunkFlushInterval
	w.mu.Unlock()
	if needFlush {
		w.Flush()
	}
	return len(p), nil
}

// Flush 立即发送缓冲区内容。
func (w *chunkWriter) Flush() {
	if w.onOutput == nil {
		return
	}
	w.mu.Lock()
	if len(w.buf) == 0 {
		w.mu.Unlock()
		return
	}
	text := string(w.buf)
	w.buf = w.buf[:0]
	w.last = time.Now()
	w.mu.Unlock()
	w.onOutput(text)
}
