package svc

import (
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
)

func TestCookieSecure(t *testing.T) {
	cases := []struct {
		mode, extURL string
		want         bool
		desc         string
	}{
		{"", "https://up.example.com", true, "auto+https 应启用 Secure"},
		{"", "http://10.0.0.1:8090", false, "auto+http 内网调试不启用"},
		{"", "", false, "auto+未配置不启用"},
		{"true", "http://x", true, "显式 true 覆盖"},
		{"false", "https://x", false, "显式 false 覆盖"},
		{"AUTO", "HTTPS://X", true, "大小写不敏感"},
	}
	for _, c := range cases {
		cfg := &config.Config{}
		cfg.Server.CookieSecure = c.mode
		cfg.Server.ExternalURL = c.extURL
		if got := cookieSecure(cfg); got != c.want {
			t.Errorf("%s: mode=%q url=%q got=%v want=%v", c.desc, c.mode, c.extURL, got, c.want)
		}
	}
	t.Log("✅ Cookie Secure 推断正确：HTTPS 部署自动启用，防会话令牌明文泄露")
}
