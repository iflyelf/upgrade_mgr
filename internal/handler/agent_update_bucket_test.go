package handler

import "testing"

// TestAutoUpdateSelected 灰度选择稳定且范围正确。
func TestAutoUpdateSelected(t *testing.T) {
	if !autoUpdateSelected("any", 100) {
		t.Fatal("100% 应全量")
	}
	if autoUpdateSelected("any", 0) {
		t.Fatal("0% 不应灰度")
	}
	// 稳定性：同一 ID 结果一致
	for _, id := range []string{"a", "agent-1", "host-42"} {
		first := autoUpdateSelected(id, 50)
		for i := 0; i < 10; i++ {
			if autoUpdateSelected(id, 50) != first {
				t.Fatalf("同一 agent %s 的结果不稳定", id)
			}
		}
	}
	// 比例粗略校验：1000 个 ID 中 30% 命中数量应落在合理范围
	hit := 0
	for i := 0; i < 1000; i++ {
		if autoUpdateSelected(string(rune('a'+i%26))+string(rune('0'+i%10))+string(rune('0'+(i/10)%10)), 30) {
			hit++
		}
	}
	if hit < 150 || hit > 450 {
		t.Fatalf("30%% 灰度命中 %d/1000，偏离过大", hit)
	}
}
