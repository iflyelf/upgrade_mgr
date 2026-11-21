package handler

import (
	"os"
	"strings"
	"testing"
)

// TestInstallMonitorScriptMetricsProbe Linux 安装脚本必须在安装/更新后
// 带超时地探测 16666/metrics，并给出成功/失败结论。
func TestInstallMonitorScriptMetricsProbe(t *testing.T) {
	data, err := os.ReadFile("../../examples/install_monitor/install-monitor.sh")
	if err != nil {
		t.Fatalf("读取安装脚本失败: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		"PROBE_TIMEOUT", "--max-time", "PROBE_RETRIES",
		"check_port_conflict", "probe_metrics", "verify_or_fail", "require_server",
		"/metrics", "16666",
		"$1成功", "监控指标探测失败",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("install-monitor.sh 缺少指标探测关键内容: %q", want)
		}
	}
	// 安装/更新都必须调用 verify_or_fail，避免只打印完成却未验证。
	if strings.Count(s, `verify_or_fail "`) < 2 {
		t.Error("install/update 都应调用 verify_or_fail")
	}
	// 平台地址必须由 --server/环境变量提供，不能内置具体域名。
	if !strings.Contains(s, `DEFAULT_SERVER=""`) {
		t.Error("install-monitor.sh 的 DEFAULT_SERVER 应为空")
	}
}
