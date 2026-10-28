// Package examples 校验示例脚本与示例配置保持一致。
package examples

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

var excludeLine = regexp.MustCompile(`(?m)^    exclude: '.*'$`)

// TestWindowsExporterDefaultConfigMatchesExample install.ps1 生成的 latest 默认配置
// 必须与 examples/windows_exporter/config.yaml 的采集器与 service.exclude 规则一致，
// 否则一键安装的主机不会使用示例里的过滤规则。
func TestWindowsExporterDefaultConfigMatchesExample(t *testing.T) {
	cfg, err := os.ReadFile("windows_exporter/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ps, err := os.ReadFile("windows_exporter/install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	// PowerShell 2.0 需要 UTF-8 BOM 才能正确解析中文。
	if !bytes.HasPrefix(ps, []byte("\xef\xbb\xbf")) {
		t.Fatal("install.ps1 必须带 UTF-8 BOM")
	}

	want := excludeLine.Find(cfg)
	if want == nil {
		t.Fatal("config.yaml 缺少 service.exclude")
	}
	got := excludeLine.Find(ps)
	if got == nil {
		t.Fatal("install.ps1 latest 默认配置缺少 service.exclude")
	}
	if !bytes.Equal(got, want) {
		t.Fatal("install.ps1 的 service.exclude 与 config.yaml 不一致，请同步")
	}

	enabled := regexp.MustCompile(`(?m)^  enabled: (.+)$`)
	cfgEnabled := enabled.FindSubmatch(cfg)
	if cfgEnabled == nil {
		t.Fatal("config.yaml 缺少 collectors.enabled")
	}
	if !bytes.Contains(ps, []byte("  enabled: "+string(cfgEnabled[1])+"\n")) {
		t.Fatalf("install.ps1 latest 采集器列表与 config.yaml 不一致: %s", cfgEnabled[1])
	}

	// exclude 正则里有 $，latest 模板必须用单引号 here-string，避免被 PowerShell 展开。
	text := string(ps)
	start := strings.Index(text, "$template = @'")
	end := strings.Index(text, "\n'@")
	if start < 0 || end < start || !strings.Contains(text[start:end], string(want)) {
		t.Fatal("latest 默认配置必须放在单引号 here-string（@' ... '@）中")
	}

	// 模板版本号与两个模板头一致，保证旧配置会被自动备份并重新生成。
	ver := regexp.MustCompile(`\$ConfigTemplateVersion = '(\d+)'`).FindStringSubmatch(text)
	if ver == nil {
		t.Fatal("缺少 $ConfigTemplateVersion")
	}
	if n := strings.Count(text, "# config-version: "+ver[1]+"\n"); n != 2 {
		t.Fatalf("latest/legacy 模板的 config-version 应都为 %s，实际匹配 %d 处", ver[1], n)
	}
}
