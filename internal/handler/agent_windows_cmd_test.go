package handler

import (
	"strings"
	"testing"
)

// TestBuildWindowsInstallCommandPowerShellSyntax 校验生成的命令是合法 PowerShell 片段。
//
// 历史 bug：psQuote 已带单引号，外层又包了一层，生成 ”url”，
// 在控制台粘贴后报「方法调用中缺少“)”」。这里做静态断言防止回归。
func TestBuildWindowsInstallCommandPowerShellSyntax(t *testing.T) {
	cmd := buildWindowsInstallCommand("http://10.1.230.50:8090", "amd64", "tok-abc", true)

	if strings.Contains(cmd, "''") {
		t.Fatalf("不应出现连续两个单引号（会破坏 PowerShell 参数）:\n%s", cmd)
	}
	for _, want := range []string{
		"$exe = Join-Path $env:TEMP 'upgrade-agent.exe'",
		"(New-Object System.Net.WebClient).DownloadFile('http://10.1.230.50:8090/api/agent/binary/windows-amd64', $exe)",
		"Start-Process -FilePath $exe -ArgumentList 'install','--server','http://10.1.230.50:8090','--enroll-token','tok-abc','--force' -Wait -NoNewWindow",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("缺少片段 %q:\n%s", want, cmd)
		}
	}
	// 每条语句单独一行，便于逐行粘贴。
	lines := strings.Split(strings.TrimSpace(cmd), "\n")
	if len(lines) != 5 {
		t.Fatalf("应为 5 行独立语句，实际 %d:\n%s", len(lines), cmd)
	}
	// 单引号必须成对（简单计数校验）。
	if strings.Count(cmd, "'")%2 != 0 {
		t.Fatalf("单引号未成对:\n%s", cmd)
	}
}

// TestBuildWindowsInstallCommandQuotesURL 含单引号的地址按 ” 转义，且不产生多余引号。
func TestBuildWindowsInstallCommandQuotesURL(t *testing.T) {
	cmd := buildWindowsInstallCommand("http://h/o'brien", "amd64", "t'k", false)
	if !strings.Contains(cmd, "DownloadFile('http://h/o''brien/api/agent/binary/windows-amd64', $exe)") {
		t.Fatalf("URL 单引号转义错误:\n%s", cmd)
	}
	if !strings.Contains(cmd, "'--enroll-token','t''k'") {
		t.Fatalf("token 单引号转义错误:\n%s", cmd)
	}
}
