//go:build windows

package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// requirePowerShell 无 PowerShell 时跳过（Server 2008 R2 ~ Windows 11 默认自带 2.0+）。
func requirePowerShell(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(powershellPath()); err == nil {
		return
	}
	if powershellPath() != "powershell.exe" {
		return
	}
	t.Skip("未找到 Windows PowerShell，跳过")
}

func runWindowsCommand(t *testing.T, script, interp, stdin string) *CommandResult {
	t.Helper()
	return ExecuteCommand(context.Background(), protocol.CommandSpec{
		Script: script, Interpreter: interp, Stdin: stdin, Timeout: "2m",
	}, nil, nil)
}

// TestWindowsPowerShellE2E 真实执行 PowerShell：输出、退出码、stdin、中文脚本解析。
//
// 这是 Windows 回归流水线的核心用例，在 windows-latest 与 Windows Server 2008 R2
// 自托管 runner 上都会执行。
func TestWindowsPowerShellE2E(t *testing.T) {
	requirePowerShell(t)

	// 1) 正常脚本（CRLF）
	res := runWindowsCommand(t, "Write-Output 'hello-windows'\r\nexit 0\r\n", "powershell", "")
	if !res.Success {
		t.Fatalf("PowerShell 应成功: %+v", res)
	}
	if !strings.Contains(res.Log, "hello-windows") {
		t.Fatalf("输出缺少标记: %q", res.Log)
	}

	// 2) 失败退出码必须透传（流水线据此判断安装失败）
	res = runWindowsCommand(t, "Write-Output 'boom'\r\nexit 7\r\n", "powershell", "")
	if res.Success {
		t.Fatal("exit 7 不应被视为成功")
	}
	if res.ExitCode != 7 {
		t.Fatalf("退出码应为 7, got %d; log=%q", res.ExitCode, res.Log)
	}

	// 3) stdin（交互确认场景）
	res = runWindowsCommand(t,
		"$line = [Console]::In.ReadLine()\r\nWrite-Output ('stdin=' + $line)\r\n", "powershell", "hello\n")
	if !res.Success || !strings.Contains(res.Log, "stdin=hello") {
		t.Fatalf("stdin 传递失败: %+v", res)
	}

	// 4) 含中文的脚本：验证 UTF-8 BOM 预处理后可正确解析（输出编码由控制台决定，
	//    这里只要求脚本成功执行，避免不同代码页导致断言不稳定）。
	res = runWindowsCommand(t, "$msg = '中文输出'\r\n# 中文注释\r\nWrite-Output $msg\r\nexit 0\r\n", "powershell", "")
	if !res.Success {
		t.Fatalf("中文脚本应能执行: %+v", res)
	}
}

// TestWindowsCmdE2E cmd.exe 命令任务路径。
func TestWindowsCmdE2E(t *testing.T) {
	res := runWindowsCommand(t, "@echo off\r\necho cmd-ok\r\nexit /b 0\r\n", "cmd", "")
	if !res.Success || !strings.Contains(res.Log, "cmd-ok") {
		t.Fatalf("cmd 执行失败: %+v", res)
	}

	res = runWindowsCommand(t, "@echo off\r\nexit /b 3\r\n", "cmd", "")
	if res.Success || res.ExitCode != 3 {
		t.Fatalf("cmd 退出码应透传: %+v", res)
	}
}

// TestPrepareScriptPathEncoding 命令脚本必须转换为 UTF-8 BOM + CRLF 的临时副本。
func TestPrepareScriptPathEncoding(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "raw.ps1")
	if err := os.WriteFile(src, []byte("# 中文注释\nWrite-Output 'x'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prepared, cleanup, err := prepareScriptPath(src)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if prepared == src {
		t.Fatal("应生成独立临时副本，避免污染原文件")
	}
	data, err := os.ReadFile(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("缺少 UTF-8 BOM: % x", data[:min(8, len(data))])
	}
	if !bytes.Contains(data, []byte("\r\n")) {
		t.Fatal("换行未转换为 CRLF")
	}
	if !bytes.Contains(data, []byte("中文注释")) {
		t.Fatal("中文内容被破坏")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
