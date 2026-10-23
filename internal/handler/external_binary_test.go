package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestFetchExternalBinaryValidatesContent 错误页/非 PE 内容必须拒绝，避免缓存脏文件。
func TestFetchExternalBinaryValidatesContent(t *testing.T) {
	dir := t.TempDir()

	t.Run("可执行文件", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("MZ\x90\x00payload"))
		}))
		defer srv.Close()
		target := filepath.Join(dir, "ok.exe")
		if err := fetchExternalBinary(context.Background(), srv.URL, target, true); err != nil {
			t.Fatalf("PE 内容应通过: %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatal("应写入缓存文件")
		}
	})

	t.Run("HTML 错误页", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<!DOCTYPE html><html>502</html>"))
		}))
		defer srv.Close()
		if err := fetchExternalBinary(context.Background(), srv.URL,
			filepath.Join(dir, "bad.exe"), true); err == nil {
			t.Fatal("HTML 页面应被拒绝")
		}
	})

	t.Run("脚本允许文本", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("# script\nWrite-Output hi\n"))
		}))
		defer srv.Close()
		target := filepath.Join(dir, "ok.ps1")
		if err := fetchExternalBinary(context.Background(), srv.URL, target, false); err != nil {
			t.Fatalf("脚本内容应通过: %v", err)
		}
	})

	t.Run("脚本拒绝 HTML", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>nope</html>"))
		}))
		defer srv.Close()
		if err := fetchExternalBinary(context.Background(), srv.URL,
			filepath.Join(dir, "bad.ps1"), false); err == nil {
			t.Fatal("HTML 脚本应被拒绝")
		}
	})
}

// TestExternalBinaryDownloadServesCached 命中缓存直接返回，且文件名规范化。
func TestExternalBinaryDownloadServesCached(t *testing.T) {
	dir := t.TempDir()
	// 预置缓存，避免测试访问外网。
	cache := filepath.Join(dir, ".external-cache")
	if err := os.MkdirAll(cache, 0755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("MZfake-exe-content")
	if err := os.WriteFile(filepath.Join(cache, "windows-exporter-amd64.exe"), payload, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Server.AgentBinaryDir = dir
	sc := &svc.ServiceContext{Config: cfg}

	req := httptest.NewRequest("GET", "/api/external/binary/windows-exporter-amd64.exe", nil)
	req.SetPathValue("name", "windows-exporter-amd64.exe")
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
}

var _ = json.Marshal
var _ = strings.TrimSpace
