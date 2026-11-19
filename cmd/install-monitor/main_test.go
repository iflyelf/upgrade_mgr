package main

import (
	"strings"
	"testing"
)

// TestTailText 失败日志只保留末尾，避免构建/安装输出过大。
func TestTailText(t *testing.T) {
	if got := tailText("abc", 10); got != "abc" {
		t.Fatalf("短文本不应截断: %q", got)
	}
	if got := tailText("0123456789", 4); got != "6789" {
		t.Fatalf("应保留末尾 4 字节: %q", got)
	}
	if got := tailText("中文日志", 4); len(got) != 4 || !strings.HasSuffix("中文日志", got) {
		t.Fatalf("按字节截断结果异常: %q", got)
	}
}

// TestMergeEnv 同名环境变量以本次传入值为准（Windows 大小写不敏感）。
func TestMergeEnv(t *testing.T) {
	got := mergeEnv([]string{"PATH=C:/x", "upgrade_mgr_external_url=http://old"}, "UPGRADE_MGR_EXTERNAL_URL=http://new")
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "http://old") || !strings.Contains(joined, "UPGRADE_MGR_EXTERNAL_URL=http://new") {
		t.Fatalf("旧环境变量未被覆盖: %v", got)
	}
	if !strings.Contains(joined, "PATH=C:/x") {
		t.Fatalf("其它环境变量应保留: %v", got)
	}
}

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
