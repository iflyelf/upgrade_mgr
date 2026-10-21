package setting

import (
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
)

func TestExternalURLSettingNormalizes(t *testing.T) {
	it, ok := registryMap["server.external_url"]
	if !ok {
		t.Fatal("server.external_url 未注册为可配置项")
	}
	cfg := &config.Config{}
	it.Set(cfg, "  https://upgrade.example.com/  ")
	if cfg.Server.ExternalURL != "https://upgrade.example.com" {
		t.Fatalf("ExternalURL = %q, want %q", cfg.Server.ExternalURL, "https://upgrade.example.com")
	}
}

func TestValidateExternalURL(t *testing.T) {
	valid := []string{
		"", // 留空表示回退到请求 Host
		"http://10.1.230.50:8090",
		"https://upgrade.example.com",
		"https://upgrade.example.com/",
		"http://[::1]:8090",
	}
	for _, v := range valid {
		if err := validateExternalURL(v); err != nil {
			t.Errorf("validateExternalURL(%q) = %v, want nil", v, err)
		}
	}

	invalid := []string{
		"upgrade.example.com",                    // 缺少协议
		"ftp://upgrade.example.com",              // 协议不支持
		"https://",                               // 缺少主机名
		"https://upgrade.example.com/path",       // 不允许路径
		"https://upgrade.example.com?x=1",        // 不允许查询参数
		"https://upgrade.example.com#frag",       // 不允许片段
		"https://user:pass@upgrade.example.com",  // 不允许用户信息
		"https://upgrade.example.com/'quote'",    // 不允许引号
		"https://upgrade.example.com/\nInjected", // 控制字符
		"https://" + strings.Repeat("a", 2049),   // 超长
	}
	for _, v := range invalid {
		if err := validateExternalURL(v); err == nil {
			t.Errorf("validateExternalURL(%q) = nil, want error", v)
		}
	}
}
