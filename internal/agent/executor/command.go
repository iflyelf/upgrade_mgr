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

const (
	// maxCommandScriptBytes 单次命令脚本大小上限（服务端另有 200KB 限制）。
	maxCommandScriptBytes = 1 << 20
	// maxCommandTimeout 单次命令最长执行时间，防止异常指令长期占用 Agent。
	maxCommandTimeout = 24 * time.Hour
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
//     （例如卸载脚本用 `dd if=/dev/tty` 读取确认）。Windows 无伪终端，忽略该项。
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
	if len(script) > maxCommandScriptBytes {
		res.Log = fmt.Sprintf("[exec] 脚本过大（%d 字节，上限 %d）\n", len(script), maxCommandScriptBytes)
		return res
	}
	timeout, _ := time.ParseDuration(strings.TrimSpace(spec.Timeout))
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if timeout > maxCommandTimeout {
		logf(protocol.StageRunCommand, "超时 %s 超过上限，已收敛为 %s", timeout, maxCommandTimeout)
		timeout = maxCommandTimeout
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

	dir := os.TempDir()
	if d := strings.TrimSpace(spec.WorkDir); d != "" {
		dir = d
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		res.Log = fmt.Sprintf("[exec] 创建工作目录失败: %v\n", err)
		return res
	}
	// 临时脚本必须带扩展名：Windows 的 PowerShell -File / cmd /c 依赖扩展名判定。
	f, err := os.CreateTemp(dir, "run-command-*"+commandScriptExt(interp))
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
	// Windows 上 Chmod 仅影响只读属性，失败不阻断执行。
	if err := os.Chmod(path, 0700); err != nil {
		logf(protocol.StageRunCommand, "设置脚本权限失败（继续执行）: %v", err)
	}

	// 与安装脚本一致：Windows PowerShell 需要 UTF-8 BOM + CRLF，
	// 否则含中文的命令脚本会按系统 ANSI(GBK) 解码而乱码/语法错误。
	prepared, cleanup, prepErr := prepareScriptPath(path)
	if prepErr != nil {
		res.Log = fmt.Sprintf("[exec] 准备脚本失败: %v\n", prepErr)
		return res
	}
	defer cleanup()

	argv := interpreterArgv(interp, prepared, spec.Args)
	useTTY := spec.TTY && ttySupported()
	if useTTY {
		argv = ttyCommand(interp, path, spec.Args)
	}

	logf(protocol.StageRunCommand, "执行命令（%s，超时 %s%s）", filepath.Base(interp), timeout,
		ttySuffix(useTTY, spec.Stdin))

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
