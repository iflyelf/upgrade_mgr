//go:build !windows

package executor

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// execLookPath 便于测试中跳过缺少 script(1) 的环境。
func execLookPath(name string) (string, error) { return exec.LookPath(name) }

// TestExecuteCommandBasic 验证脚本通过解释器执行并回传输出。
func TestExecuteCommandBasic(t *testing.T) {
	var stages []string
	var out strings.Builder
	res := ExecuteCommand(context.Background(), protocol.CommandSpec{
		Script: "echo hello-command\nexit 0",
	}, func(chunk string) { out.WriteString(chunk) },
		func(stage, msg string) { stages = append(stages, stage) })
	if !res.Success || res.ExitCode != 0 {
		t.Fatalf("命令应执行成功: %+v", res)
	}
	if !strings.Contains(res.Log, "hello-command") {
		t.Fatalf("日志缺少 stdout: %q", res.Log)
	}
	if !strings.Contains(out.String(), "hello-command") {
		t.Fatalf("流式输出缺少 stdout: %q", out.String())
	}
	if len(stages) == 0 || stages[0] != protocol.StageRunCommand {
		t.Fatalf("应先上报执行命令阶段: %v", stages)
	}
}

// TestExecuteCommandArgsStdin 验证参数与标准输入（交互确认）。
func TestExecuteCommandArgsStdin(t *testing.T) {
	res := ExecuteCommand(context.Background(), protocol.CommandSpec{
		Script: "read -r ans\necho \"arg=$1 ans=$ans\"",
		Args:   []string{"uninstall"},
		Stdin:  "Y\n",
	}, nil, nil)
	if !res.Success {
		t.Fatalf("命令应执行成功: %+v", res)
	}
	if !strings.Contains(res.Log, "arg=uninstall ans=Y") {
		t.Fatalf("参数/标准输入未生效: %q", res.Log)
	}
}

// TestExecuteCommandExitCode 非零退出码默认失败，ignore_error 时视为成功。
func TestExecuteCommandExitCode(t *testing.T) {
	res := ExecuteCommand(context.Background(), protocol.CommandSpec{Script: "exit 3"}, nil, nil)
	if res.Success || res.ExitCode != 3 {
		t.Fatalf("应返回失败和退出码 3: %+v", res)
	}
	res = ExecuteCommand(context.Background(), protocol.CommandSpec{Script: "exit 3", IgnoreError: true}, nil, nil)
	if !res.Success {
		t.Fatalf("ignore_error 应视为成功: %+v", res)
	}
}

// TestExecuteCommandRejectsInterpreter 解释器白名单。
func TestExecuteCommandRejectsInterpreter(t *testing.T) {
	res := ExecuteCommand(context.Background(), protocol.CommandSpec{
		Script: "echo hi", Interpreter: "/usr/bin/python3",
	}, nil, nil)
	if res.Success || !strings.Contains(res.Log, "不支持的解释器") {
		t.Fatalf("非白名单解释器应被拒绝: %+v", res)
	}
}

// TestExecuteCommandTTY 通过 script(1) 分配伪终端，兼容读取 /dev/tty 的脚本。
func TestExecuteCommandTTY(t *testing.T) {
	if _, err := execLookPath("script"); err != nil {
		t.Skip("目标环境缺少 script(1)")
	}
	res := ExecuteCommand(context.Background(), protocol.CommandSpec{
		// 从 /dev/tty 读一个字符；stdin 已接到伪终端。
		Script: "dd if=/dev/tty bs=1 count=1 2>/dev/null\necho done",
		Stdin:  "Y\n",
		TTY:    true,
	}, nil, nil)
	if !res.Success {
		t.Fatalf("伪终端执行应成功: %+v", res)
	}
	if !strings.Contains(res.Log, "done") {
		t.Fatalf("缺少脚本输出: %q", res.Log)
	}
}

// TestShellQuote 参数转义不能产生 shell 注入。
func TestShellQuote(t *testing.T) {
	got := shellQuote("a'b c")
	want := `'a'\''b c'`
	if got != want {
		t.Fatalf("shellQuote(%q)=%q want %q", "a'b c", got, want)
	}
}

// TestTTYCommandReturnCode 伪终端模式必须尽量透传子命令退出码。
//
// 老版本 script(1) 没有 -e，会总是返回 0，导致失败的脚本被标记为成功。
// 支持 -e 时必须带上；命令字符串整体作为一个参数传入，不经过额外 shell。
func TestTTYCommandReturnCode(t *testing.T) {
	argv := ttyCommand("/bin/bash", "/tmp/x.sh", []string{"uninstall"})
	if len(argv) < 5 || argv[0] != "script" {
		t.Fatalf("非预期 argv: %v", argv)
	}
	hasE := false
	for _, a := range argv[1:] {
		if a == "-e" {
			hasE = true
		}
	}
	if hasE != scriptSupportsReturn() {
		t.Fatalf("-e 应与探测结果一致: got=%v want=%v argv=%v", hasE, scriptSupportsReturn(), argv)
	}
	if argv[len(argv)-1] != "/dev/null" {
		t.Fatalf("typescript 必须指向 /dev/null: %v", argv)
	}
	if argv[len(argv)-2] != "'/bin/bash' '/tmp/x.sh' 'uninstall'" {
		t.Fatalf("命令参数拼装错误: %q", argv[len(argv)-2])
	}
}

// TestExecuteCommandRejectsOversizedScript 超限脚本直接拒绝，避免内存/磁盘压力。
func TestExecuteCommandRejectsOversizedScript(t *testing.T) {
	res := ExecuteCommand(context.Background(), protocol.CommandSpec{
		Script: strings.Repeat("a", maxCommandScriptBytes+1),
	}, nil, nil)
	if res.Success {
		t.Fatal("超限脚本不应执行")
	}
	if !strings.Contains(res.Log, "脚本过大") {
		t.Fatalf("应提示脚本过大: %q", res.Log)
	}
}
