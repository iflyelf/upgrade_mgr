package main

import (
	"testing"
)

// TestParseBinPathDir 从 sc qc 输出解析服务安装目录，兼容带引号/不带引号路径。
func TestParseBinPathDir(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"带引号", "SERVICE_NAME: windows_exporter\n        BINARY_PATH_NAME   : \"C:\\monitor\\exporter\\windows_exporter.exe\" --config.file=\"C:\\monitor\\exporter\\config.yaml\"", `C:\monitor\exporter`},
		{"不带引号", "BINARY_PATH_NAME : C:\\Program Files\\windows_exporter\\windows_exporter.exe --config.file=x", `C:\Program Files\windows_exporter`},
		{"无服务", "FAILED 1060", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseBinPathDir(c.out); got != c.want {
				t.Fatalf("parseBinPathDir=%q, want %q", got, c.want)
			}
		})
	}
}

// TestWindowsDir 兼容 Windows 反斜杠路径。
func TestWindowsDir(t *testing.T) {
	if got := windowsDir(`C:\monitor\exporter\windows_exporter.exe`); got != `C:\monitor\exporter` {
		t.Fatalf("windowsDir=%q", got)
	}
	if got := windowsDir(`windows_exporter.exe`); got != "" {
		t.Fatalf("无目录时应为空: %q", got)
	}
}

// TestResolveServerNoDefault 平台地址必须来自参数/环境变量，不能有内置默认值。
func TestResolveServerNoDefault(t *testing.T) {
	t.Setenv("UPGRADE_MGR_EXTERNAL_URL", "")
	t.Setenv("UPGRADE_AGENT_SERVER_URL", "")
	t.Setenv("UPGRADE_MGR_SERVER", "")
	if got := resolveServer(""); got != "" {
		t.Fatalf("未指定时应为空，实际 %q", got)
	}
	if got := resolveServer("mgr.example.com"); got != "http://mgr.example.com" {
		t.Fatalf("无协议时应补 http://，实际 %q", got)
	}
	t.Setenv("UPGRADE_MGR_EXTERNAL_URL", "https://ext.example.com/")
	if got := resolveServer(""); got != "https://ext.example.com" {
		t.Fatalf("应读取外部地址环境变量，实际 %q", got)
	}
}
