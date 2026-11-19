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
