package setting

import (
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
)

// TestExternalBinariesSettingValidate 白名单设置项：解析、校验、热加载写回。
func TestExternalBinariesSettingValidate(t *testing.T) {
	it := registryMap["server.external_binaries"]
	if it.Key == "" {
		t.Fatal("缺少 server.external_binaries 设置项")
	}

	// 合法配置
	valid := "custom-tool=https://github.com/example/tool.exe\n" +
		"custom-install.ps1=script:https://raw.githubusercontent.com/example/install.ps1"
	if it.Validate != nil {
		if err := it.Validate(valid); err != nil {
			t.Fatalf("合法白名单应通过: %v", err)
		}
	}
	cfg := config.Default()
	it.Set(cfg, valid)
	if len(cfg.Server.ExternalBinaries) != 2 {
		t.Fatalf("应写入 2 条，实际 %d: %v", len(cfg.Server.ExternalBinaries), cfg.Server.ExternalBinaries)
	}
	if got := it.Get(cfg); !strings.Contains(got, "custom-tool=") {
		t.Fatalf("Get 应返回可编辑文本: %q", got)
	}

	// 非法：缺少 =、非白名单域名
	if it.Validate == nil {
		t.Fatal("应配置 Validate")
	}
	if err := it.Validate("bad-line"); err == nil {
		t.Fatal("缺少 = 应报错")
	}
	// 域名白名单校验发生在 Apply（需要结合本次提交的 hosts），Item.Validate 只校验格式。
	if err := it.Validate("x=https://evil.example.com/tool.exe"); err != nil {
		t.Fatalf("Item.Validate 只校验格式，不应因域名报错: %v", err)
	}
	if err := extbin.ValidateConfig(map[string]string{"x": "https://evil.example.com/tool.exe"}, nil); err == nil {
		t.Fatal("非白名单域名应被 ValidateConfig 拒绝")
	}
}

// TestExternalBinaryHostsSetting 域名白名单与 TTL 校验。
func TestExternalBinaryHostsSetting(t *testing.T) {
	hosts := registryMap["server.external_binary_hosts"]
	cfg := config.Default()
	hosts.Set(cfg, "mirror.corp, cdn.example.com")
	if len(cfg.Server.ExternalBinaryHosts) != 2 ||
		cfg.Server.ExternalBinaryHosts[0] != "mirror.corp" {
		t.Fatalf("域名解析错误: %v", cfg.Server.ExternalBinaryHosts)
	}
	hosts.Set(cfg, "")
	if cfg.Server.ExternalBinaryHosts != nil {
		t.Fatalf("清空后应为 nil（回退默认）: %v", cfg.Server.ExternalBinaryHosts)
	}

	ttl := registryMap["server.external_binary_ttl"]
	if err := ttl.Validate("24h"); err != nil {
		t.Fatalf("24h 应通过: %v", err)
	}
	if err := ttl.Validate("abc"); err == nil {
		t.Fatal("非法时长应报错")
	}
	if err := ttl.Validate("-1h"); err == nil {
		t.Fatal("负数时长应报错")
	}
	if err := ttl.Validate(""); err != nil {
		t.Fatalf("留空应通过: %v", err)
	}
}

// TestSnapshotCopiesExternalBinaries 快照必须深拷贝 map，避免并发读写。
func TestSnapshotCopiesExternalBinaries(t *testing.T) {
	cfg := config.Default()
	cfg.Server.ExternalBinaries = map[string]string{"a": "https://github.com/a"}
	s := NewService(nil, cfg)
	snap := s.Snapshot()
	snap.Server.ExternalBinaries["a"] = "modified"
	snap.Server.ExternalBinaries["b"] = "new"

	cur := s.Snapshot()
	if cur.Server.ExternalBinaries["a"] != "https://github.com/a" {
		t.Fatalf("快照应深拷贝 map，实际被修改: %v", cur.Server.ExternalBinaries)
	}
	if _, ok := cur.Server.ExternalBinaries["b"]; ok {
		t.Fatal("快照新增键不应影响原配置")
	}
}
