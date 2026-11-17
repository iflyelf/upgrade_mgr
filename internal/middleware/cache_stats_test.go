package middleware

import "testing"

// TestAgentStatsNotCached 版本分布必须每次刷新都读最新数据。
//
// Agent 心跳/自升级结果走 /api/agent/*，不会触发页面缓存失效；若统计接口
// 仍走 Redis 缓存，数量/离线/状态会滞后一个 TTL，看起来像没有自动刷新。
func TestAgentStatsNotCached(t *testing.T) {
	if cacheablePath("/api/agents/stats") {
		t.Fatal("/api/agents/stats 不应缓存：Agent 上报不会清空页面缓存，版本分布会滞后")
	}
	for _, p := range []string{"/api/hosts", "/api/tasks"} {
		if !cacheablePath(p) {
			t.Fatalf("%s 应继续使用页面缓存", p)
		}
	}
}
