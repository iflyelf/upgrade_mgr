package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExporterBinPath 按通道选择 textfile 参数，并固定安装目录/端口。
func TestExporterBinPath(t *testing.T) {
	latest := exporterBinPath("latest")
	if !strings.Contains(latest, "--collector.textfile.directories=") {
		t.Fatalf("latest 应使用 directories: %s", latest)
	}
	legacy := exporterBinPath("legacy")
	if !strings.Contains(legacy, "--collector.textfile.directory=") ||
		strings.Contains(legacy, "--collector.textfile.directories=") {
		t.Fatalf("legacy 应使用单数 directory: %s", legacy)
	}
	for _, p := range []string{latest, legacy} {
		if !strings.Contains(p, exporterExePath) ||
			!strings.Contains(p, "--web.listen-address=:16666") {
			t.Fatalf("服务命令行缺少安装路径/端口: %s", p)
		}
	}
}

// TestEmbeddedExporterConfigs 内置配置必须覆盖两个通道的关键采集器。
func TestEmbeddedExporterConfigs(t *testing.T) {
	latest, err := exporterConfigFS.ReadFile("templates/latest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(latest), "physical_disk") ||
		!strings.Contains(string(latest), "C:\\monitor\\exporter\\metrics") {
		t.Fatal("latest 配置缺少 physical_disk/textfile 目录")
	}
	legacy, err := exporterConfigFS.ReadFile("templates/legacy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(legacy), "hardware_info") {
		t.Fatal("legacy 配置缺少 hardware_info")
	}
}

// TestIsPEBinary 只接受 MZ 头且大于 1MB 的文件，拦截平台错误页。
func TestIsPEBinary(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.exe")
	data := make([]byte, 1<<20+2)
	data[0], data[1] = 'M', 'Z'
	if err := os.WriteFile(ok, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !isPEBinary(ok) {
		t.Fatal("有效 PE 应通过")
	}
	bad := filepath.Join(dir, "bad.exe")
	if err := os.WriteFile(bad, []byte("<html>error</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isPEBinary(bad) {
		t.Fatal("HTML 错误页不应通过")
	}
}

// TestEmbeddedConfigMatchesExamples 安装器内置配置必须与示例配置一致，避免两处漂移。
func TestEmbeddedConfigMatchesExamples(t *testing.T) {
	for _, c := range []struct {
		embedded, example string
	}{
		{"templates/latest.yaml", "../../examples/windows_exporter/config.yaml"},
		{"templates/legacy.yaml", "../../examples/windows_exporter/config-legacy.yaml"},
	} {
		got, err := exporterConfigFS.ReadFile(c.embedded)
		if err != nil {
			t.Fatalf("读取内置配置失败: %v", err)
		}
		want, err := os.ReadFile(c.example)
		if err != nil {
			t.Fatalf("读取示例配置失败: %v", err)
		}
		if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
			t.Fatalf("%s 与 %s 不一致，请同步更新安装器内置配置", c.embedded, c.example)
		}
	}
}
