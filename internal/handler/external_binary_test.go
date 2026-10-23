package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

func TestValidateExternalContent(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// 可执行文件：必须 MZ 开头
	if err := validateExternalContent(write("ok.exe", "MZ\x90\x00payload"), false); err != nil {
		t.Fatalf("PE 内容应通过: %v", err)
	}
	if err := validateExternalContent(write("bad.exe", "<!DOCTYPE html><html></html>"), false); err == nil {
		t.Fatal("HTML 冒充 exe 应被拒绝")
	}
	if err := validateExternalContent(write("empty.exe", ""), false); err == nil {
		t.Fatal("空内容应被拒绝")
	}

	// 脚本：允许文本，拒绝 HTML/XML
	if err := validateExternalContent(write("ok.ps1", "# script\nWrite-Output hi\n"), true); err != nil {
		t.Fatalf("脚本内容应通过: %v", err)
	}
	for _, html := range []string{
		"<!DOCTYPE html><html></html>",
		"<html><body>x</body></html>",
		"<?xml version=\"1.0\"?>",
	} {
		if err := validateExternalContent(write("bad.ps1", html), true); err == nil {
			t.Fatalf("HTML/XML 脚本应被拒绝: %q", html)
		}
	}
}

// TestExternalBinaryDownloadServesCached 命中缓存直接返回，白名单外 404。
func TestExternalBinaryDownloadServesCached(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, ".external-cache")
	if err := os.MkdirAll(cache, 0755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("MZfake-exe-content")
	if err := os.WriteFile(filepath.Join(cache, "custom-tool.exe"), payload, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Server.AgentBinaryDir = dir
	cfg.Server.ExternalBinaries = map[string]string{
		"custom-tool": "https://github.com/example/custom-tool.exe",
	}
	sc := &svc.ServiceContext{Config: cfg}

	req := httptest.NewRequest("GET", "/api/external/binary/custom-tool.exe", nil)
	req.SetPathValue("name", "custom-tool.exe")
	rec := httptest.NewRecorder()
	ExternalBinaryDownload(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应命中缓存，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(payload) {
		t.Fatalf("内容不符: %q", rec.Body.String())
	}

	// 白名单外必须 404
	req = httptest.NewRequest("GET", "/api/external/binary/evil", nil)
	req.SetPathValue("name", "evil")
	rec = httptest.NewRecorder()
	ExternalBinaryDownload(sc)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("白名单外应 404，实际 %d", rec.Code)
	}

	// 非法名称（含路径分隔符）必须 404
	req = httptest.NewRequest("GET", "/api/external/binary/a%2Fb", nil)
	req.SetPathValue("name", "a/b")
	rec = httptest.NewRecorder()
	ExternalBinaryDownload(sc)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非法名称应 404，实际 %d", rec.Code)
	}
}

// TestEffectiveExternalBinariesFallsBackToDefault 未配置时使用内置默认（升级兼容）。
func TestEffectiveExternalBinariesFallsBackToDefault(t *testing.T) {
	cfg := config.Default()
	got := effectiveExternalBinaries(cfg)
	if len(got) != 5 {
		t.Fatalf("默认应有 5 条，实际 %d", len(got))
	}
	for _, name := range []string{"windows-exporter-amd64", "windows-exporter-amd64-legacy", "windows-exporter-arm64-legacy"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("默认应包含 %s", name)
		}
	}
	if !strings.Contains(got["windows-exporter-amd64-legacy"], "windows_exporter-amd64-legacy.exe") {
		t.Fatalf("legacy 地址不符: %s", got["windows-exporter-amd64-legacy"])
	}

	// 配置了就用配置（完全覆盖，不与默认合并）
	cfg.Server.ExternalBinaries = map[string]string{"only": "https://github.com/x"}
	got = effectiveExternalBinaries(cfg)
	if len(got) != 1 || got["only"] == "" {
		t.Fatalf("应使用配置项: %v", got)
	}
}

// TestFetchExternalBinaryRejectsNonWhitelistedHost 上游域名不在白名单时拒绝。
func TestFetchExternalBinaryRejectsNonWhitelistedHost(t *testing.T) {
	err := fetchExternalBinary(context.Background(),
		"https://evil.example.com/x.exe", filepath.Join(t.TempDir(), "x.exe"), false, nil, extbin.AllowRule{})
	if err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("应拒绝非白名单域名，got %v", err)
	}
}

// TestFetchExternalBinaryRejectsInternalHost 内网地址即使加进域名白名单也拒绝。
func TestFetchExternalBinaryRejectsInternalHost(t *testing.T) {
	hosts := []string{"127.0.0.1"}
	err := fetchExternalBinary(context.Background(),
		"http://127.0.0.1:1/x.exe", filepath.Join(t.TempDir(), "x.exe"), false, hosts, extbin.AllowRule{})
	if err == nil || !strings.Contains(err.Error(), "内网") {
		t.Fatalf("应拒绝内网地址，got %v", err)
	}
}
