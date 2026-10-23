package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestExternalBinaryMetrics 校验外部文件相关指标已注册且可采集。
//
// 不使用 testutil（会引入额外依赖），直接 Gather 默认注册表按名称查找。
func TestExternalBinaryMetrics(t *testing.T) {
	ExternalBinaryUp.WithLabelValues("probe-ok", "binary").Set(1)
	ExternalBinaryUp.WithLabelValues("probe-bad", "script").Set(0)
	ExternalBinaryCached.WithLabelValues("probe-ok").Set(1)
	ExternalBinaryCachedBytes.WithLabelValues("probe-ok").Set(1024)
	ExternalBinaryProbeTotal.WithLabelValues("probe-ok", "success").Inc()
	ExternalBinaryRequests.WithLabelValues("probe-ok", "fresh").Inc()
	ExternalBinaryBytes.WithLabelValues("probe-ok").Add(2048)
	// Histogram 只有 Observe 后才会出现在 Gather 结果里。
	ExternalBinaryProbeSeconds.WithLabelValues("probe-ok").Set(0.2)
	ExternalBinaryDuration.WithLabelValues("probe-ok", "success").Observe(0.3)

	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	want := map[string]bool{
		"upgrade_mgr_external_binary_up":               false,
		"upgrade_mgr_external_binary_cached":           false,
		"upgrade_mgr_external_binary_cached_bytes":     false,
		"upgrade_mgr_external_binary_probe_total":      false,
		"upgrade_mgr_external_binary_requests_total":   false,
		"upgrade_mgr_external_binary_bytes_total":      false,
		"upgrade_mgr_external_binary_probe_seconds":    false,
		"upgrade_mgr_external_binary_download_seconds": false,
	}
	for _, mf := range mfs {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("指标未出现: %s", name)
		}
	}

	// up 指标应能按 name/type 区分
	for _, mf := range mfs {
		if mf.GetName() != "upgrade_mgr_external_binary_up" {
			continue
		}
		found := false
		for _, m := range mf.GetMetric() {
			if labelsOf(m) == "name=probe-ok,type=binary" && m.GetGauge().GetValue() == 1 {
				found = true
			}
		}
		if !found {
			t.Fatal("缺少 up{name=probe-ok,type=binary}=1")
		}
	}
}

// labelsOf 拼装有序标签串用于断言。
func labelsOf(m *dto.Metric) string {
	out := ""
	for _, lp := range m.GetLabel() {
		if out != "" {
			out += ","
		}
		out += lp.GetName() + "=" + lp.GetValue()
	}
	return out
}
