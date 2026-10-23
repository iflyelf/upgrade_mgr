package agentrun

import (
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

func TestApplyServerURLTunnelSwitchAndFallback(t *testing.T) {
	a := New(Config{
		ServerURL:        "http://10.0.0.5:8090",
		DefaultServerURL: "http://10.0.0.5:8090",
	})
	// 纯 IP：沿用默认协议与端口
	if !a.applyServerURL("10.8.0.9") {
		t.Fatal("隧道地址应触发切换")
	}
	if a.cfg.ServerURL != "http://10.8.0.9:8090" {
		t.Fatalf("ServerURL=%q", a.cfg.ServerURL)
	}
	// 相同地址不重复切换
	if a.applyServerURL("http://10.8.0.9:8090/") {
		t.Fatal("相同地址不应算变化")
	}
	// 清空 → 回退默认
	if !a.applyServerURL("") {
		t.Fatal("清空隧道应回退默认地址")
	}
	if a.cfg.ServerURL != "http://10.0.0.5:8090" {
		t.Fatalf("未回退默认地址: %q", a.cfg.ServerURL)
	}
	// 非法协议被忽略
	if a.applyServerURL("file:///etc/passwd") {
		t.Fatal("非 http(s) 应被忽略")
	}
}

func TestTunnelFailureFallsBack(t *testing.T) {
	a := New(Config{
		ServerURL:        "http://10.8.0.9:8090",
		DefaultServerURL: "http://10.0.0.5:8090",
	})
	for i := 0; i < 2; i++ {
		a.handleConnectFailure()
		if a.cfg.ServerURL != "http://10.8.0.9:8090" {
			t.Fatalf("第 %d 次失败不应立即回退", i+1)
		}
	}
	a.handleConnectFailure()
	if a.cfg.ServerURL != "http://10.0.0.5:8090" {
		t.Fatalf("连续 3 次失败应回退，实际 %q", a.cfg.ServerURL)
	}
	// 冷却期内不得立即切回刚失败的隧道地址，避免连接抖动。
	if a.applyServerURL("10.8.0.9") {
		t.Fatal("冷却期内不应重新切换到失败的隧道地址")
	}
	if a.cfg.ServerURL != "http://10.0.0.5:8090" {
		t.Fatalf("冷却期内地址不应变化: %q", a.cfg.ServerURL)
	}
}

func TestNewKeepsDefaultServerURL(t *testing.T) {
	// 旧 state 没有 default_server_url：构造时用当前地址补齐。
	a := New(Config{ServerURL: "https://mgr.example.com/"})
	if a.cfg.DefaultServerURL != "https://mgr.example.com" {
		t.Fatalf("DefaultServerURL=%q", a.cfg.DefaultServerURL)
	}
	// 已保存默认地址时不能被当前隧道地址覆盖。
	b := New(Config{ServerURL: "http://10.8.0.9:8090", DefaultServerURL: "http://10.0.0.5:8090"})
	if b.cfg.DefaultServerURL != "http://10.0.0.5:8090" {
		t.Fatalf("默认地址被覆盖: %q", b.cfg.DefaultServerURL)
	}
}

func TestHeartbeatResponseTunnelResolution(t *testing.T) {
	// 服务端下发纯 IP 时，Agent 应补齐默认端口。
	a := New(Config{ServerURL: "https://mgr.example.com", DefaultServerURL: "https://mgr.example.com"})
	got := a.resolveServerURL("10.8.0.9")
	if got != "https://10.8.0.9:443" {
		t.Fatalf("resolveServerURL=%q", got)
	}
	var _ protocol.HeartbeatResponse
}

// TestServerURLFailover 默认地址连续失败时轮换到备用地址。
func TestServerURLFailover(t *testing.T) {
	a := New(Config{
		ServerURL:        "https://a.example.com",
		DefaultServerURL: "https://a.example.com",
		ServerURLs:       []string{"https://a.example.com", "https://b.example.com", "https://c.example.com"},
	})
	// 默认地址连续 2 次失败后切到 b
	a.handleConnectFailure()
	if a.cfg.ServerURL != "https://a.example.com" {
		t.Fatalf("第一次失败不应切换: %q", a.cfg.ServerURL)
	}
	a.handleConnectFailure()
	if a.cfg.ServerURL != "https://b.example.com" {
		t.Fatalf("应切换到备用地址 b，实际 %q", a.cfg.ServerURL)
	}
	// b 是备用地址（非默认），连续 3 次失败回退默认
	a.handleConnectFailure()
	a.handleConnectFailure()
	a.handleConnectFailure()
	if a.cfg.ServerURL != "https://a.example.com" {
		t.Fatalf("备用地址失败应回退默认，实际 %q", a.cfg.ServerURL)
	}
	// 再次失败继续轮换
	a.handleConnectFailure()
	a.handleConnectFailure()
	if a.cfg.ServerURL != "https://b.example.com" {
		t.Fatalf("应继续轮换，实际 %q", a.cfg.ServerURL)
	}
}

// TestNormalizeServerList 当前地址始终包含在列表中且去重。
func TestNormalizeServerList(t *testing.T) {
	got := normalizeServerList("https://a/", []string{"https://b", "https://a", "", "https://b/"})
	want := []string{"https://a", "https://b"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}
