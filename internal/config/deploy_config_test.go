package config

import "testing"

// TestLoadDeployConfigDefaults 示例配置可被解析，且外部文件白名单默认为空（用内置默认）。
func TestLoadDeployConfigDefaults(t *testing.T) {
	c, err := Load("../../deploy/config/config.yaml")
	if err != nil {
		t.Fatalf("加载示例配置失败: %v", err)
	}
	if c.Server.ExternalBinaryTTL != "24h" {
		t.Fatalf("ttl=%q", c.Server.ExternalBinaryTTL)
	}
	if c.Server.ExternalBinaryProxy != "https://proxy.xiaonuo.live/proxy?url=" {
		t.Fatalf("proxy=%q", c.Server.ExternalBinaryProxy)
	}
	if len(c.Server.ExternalBinaryProxyHosts) == 0 {
		t.Fatal("默认应包含走代理的上游域名")
	}
	if len(c.Server.ExternalBinaries) != 0 {
		t.Fatalf("示例配置应为空白名单（使用内置默认）: %v", c.Server.ExternalBinaries)
	}
}
