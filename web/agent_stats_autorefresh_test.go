package web

import (
	"os"
	"strings"
	"testing"
)

// TestAgentStatsAutoRefreshWithHosts 版本分布卡片必须跟随主机列表一起自动刷新。
//
// 历史实现里版本分布有独立的 30 秒 / 10 秒节流，主机列表每 15 秒刷新时
// 看起来卡片长期不动。这里锁定：loadHosts 每次都会调用 loadAgentStats，
// 且卡片带有「自动刷新中」提示。
func TestAgentStatsAutoRefreshWithHosts(t *testing.T) {
	src, err := os.ReadFile("dist/assets/app.js")
	if err != nil {
		t.Fatalf("读取 app.js 失败: %v", err)
	}
	body := string(src)

	start := strings.Index(body, "function loadHosts(page, quiet)")
	if start < 0 {
		t.Fatal("app.js 中找不到 loadHosts()")
	}
	end := strings.Index(body[start:], "var seq = ++hostsReqSeq;")
	if end < 0 {
		t.Fatal("loadHosts() 结构变化，请同步更新本测试")
	}
	loadHosts := body[start : start+end]
	if !strings.Contains(loadHosts, "loadAgentStats();") {
		t.Error("loadHosts() 没有调用 loadAgentStats()，版本分布不会跟随主机列表自动刷新")
	}
	for _, banned := range []string{"statsEvery", "agentStatsAt >= "} {
		if strings.Contains(loadHosts, banned) {
			t.Errorf("loadHosts() 仍包含独立的版本分布节流逻辑 %q，版本分布应与主机列表同频刷新", banned)
		}
	}

	cardStart := strings.Index(body, "function agentStatsCard()")
	if cardStart < 0 {
		t.Fatal("app.js 中找不到 agentStatsCard()")
	}
	cardEnd := strings.Index(body[cardStart:], "function shortHash(")
	if cardEnd < 0 {
		t.Fatal("agentStatsCard() 结构变化，请同步更新本测试")
	}
	if !strings.Contains(body[cardStart:cardStart+cardEnd], "auto-refresh-tip") {
		t.Error("agentStatsCard() 缺少「自动刷新中」提示")
	}
}
