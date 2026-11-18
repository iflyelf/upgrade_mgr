package handler

import (
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
)

// TestExternalAllowedHostsBuiltinScript 内置安装脚本的上游域名始终放行。
//
// 管理员把 external_binary_hosts 收紧到只有 github.com 时，
// install-monitor.sh 仍能从 raw.githubusercontent.com 更新；
// 自定义条目则仍严格受白名单限制。
func TestExternalAllowedHostsBuiltinScript(t *testing.T) {
	cfg := config.Default()
	cfg.Server.ExternalBinaryHosts = []string{"github.com"}

	entries, err := extbin.Parse(effectiveExternalBinaries(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var builtin extbin.Entry
	for _, e := range entries {
		if e.Name == "install-monitor.sh" {
			builtin = e
		}
	}
	if builtin.Name == "" {
		t.Fatal("未找到 install-monitor.sh 内置条目")
	}
	has := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	if !has(externalAllowedHosts(cfg, builtin), "raw.githubusercontent.com") {
		t.Fatal("内置脚本域名应始终放行")
	}

	custom := extbin.Entry{Name: "custom-script.sh", URL: "https://mirror.example.org/x.sh", Script: true}
	if has(externalAllowedHosts(cfg, custom), "mirror.example.org") {
		t.Fatal("自定义条目的域名不应被隐式放行")
	}
}
