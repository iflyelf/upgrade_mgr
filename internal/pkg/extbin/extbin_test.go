package extbin

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	entries, err := Parse(map[string]string{
		"win-exporter.exe": "https://github.com/a/b.exe",
		"win-install.ps1":  "script:https://raw.githubusercontent.com/a/b.ps1",
		"WIN_UPPER":        "https://proxy.xiaonuo.live/?url=x",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(entries))
	}
	// 排序稳定：WIN_UPPER 在前（大写 ASCII 小于小写）
	if entries[0].Name != "WIN_UPPER" {
		t.Fatalf("应按名称排序，实际首项 %q", entries[0].Name)
	}
	var script *Entry
	for i := range entries {
		if entries[i].Name == "win-install.ps1" {
			script = &entries[i]
		}
	}
	if script == nil || !script.Script {
		t.Fatalf("script: 前缀应标记为脚本: %+v", script)
	}
	if !strings.HasPrefix(script.URL, "https://") {
		t.Fatalf("应去掉 script: 前缀: %q", script.URL)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	bad := []map[string]string{
		{"a/b": "https://github.com/x"},          // 名称含路径分隔符
		{"ok": "ftp://github.com/x"},             // 协议不支持
		{"ok": "https://user:pass@github.com/x"}, // 带凭据
		{"ok": ""},                               // 空地址
		{"": "https://github.com/x"},             // 空名称
	}
	for _, m := range bad {
		if _, err := Parse(m); err == nil {
			t.Errorf("应拒绝 %+v", m)
		}
	}
}

func TestParseTextAndFormat(t *testing.T) {
	text := `
# comment
windows-exporter-amd64=https://github.com/a.exe
windows-exporter-install.ps1=script:https://raw.githubusercontent.com/b.ps1
`
	m, err := ParseText(text)
	if err != nil {
		t.Fatalf("ParseText: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("应解析 2 条，实际 %d: %v", len(m), m)
	}
	round := FormatText(m)
	if !strings.Contains(round, "windows-exporter-amd64=https://github.com/a.exe") ||
		!strings.Contains(round, "script:https://raw.githubusercontent.com/b.ps1") {
		t.Fatalf("格式化结果不符:\n%s", round)
	}
	if _, err := ParseText("bad-line-without-equals"); err == nil {
		t.Fatal("缺少 = 应报错")
	}
}

func TestHostAllowed(t *testing.T) {
	ok := []string{
		"https://github.com/a/b.exe",
		"https://raw.githubusercontent.com/a/b.ps1",
		"https://proxy.xiaonuo.live/?url=x",
	}
	for _, u := range ok {
		if err := HostAllowed(u, nil); err != nil {
			t.Errorf("默认白名单应允许 %s: %v", u, err)
		}
	}
	if err := HostAllowed("https://evil.example.com/x", nil); err == nil {
		t.Fatal("非白名单域名应拒绝")
	}
	// 自定义白名单
	if err := HostAllowed("https://mirror.corp/x", []string{"mirror.corp"}); err != nil {
		t.Fatalf("自定义域名应允许: %v", err)
	}
	// 带端口的配置项也能匹配（比较时忽略端口）
	if err := HostAllowed("https://mirror.corp/x", []string{"mirror.corp:8443"}); err != nil {
		t.Fatalf("带端口配置应匹配: %v", err)
	}
}

func TestEnsureNotInternal(t *testing.T) {
	internal := []string{
		"http://127.0.0.1/x",
		"http://10.0.0.5/x",
		"http://192.168.1.1/x",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/x",
	}
	for _, u := range internal {
		if err := EnsureNotInternal(u); err == nil {
			t.Errorf("应拒绝内网地址 %s", u)
		}
	}
}

func TestValidateConfig(t *testing.T) {
	if err := ValidateConfig(map[string]string{
		"a": "https://github.com/x.exe",
	}, nil); err != nil {
		t.Fatalf("合法配置应通过: %v", err)
	}
	if err := ValidateConfig(map[string]string{
		"a": "https://evil.example.com/x.exe",
	}, nil); err == nil {
		t.Fatal("非白名单域名应拒绝")
	}
}
