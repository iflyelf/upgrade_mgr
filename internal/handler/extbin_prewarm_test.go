package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestPrewarmExternalBinaries 预热：未缓存的条目自动下载；已上传离线文件或缓存未过期的跳过。
//
// 上游域名只放行 127.0.0.1：内置条目（github.com）在域名校验处立即失败，测试不访问外网。
func TestPrewarmExternalBinaries(t *testing.T) {
	var hits int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("#!/bin/bash\necho ok\n"))
	}))
	defer origin.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.AgentBinaryDir = dir
	cfg.Server.ExternalBinaries = map[string]string{
		"fresh-script.sh":  "script:" + origin.URL + "/a.sh",
		"local-script.sh":  "script:" + origin.URL + "/b.sh",
		"cached-script.sh": "script:" + origin.URL + "/c.sh",
	}
	cfg.Server.ExternalBinaryHosts = []string{"127.0.0.1"}
	cfg.Server.ExternalBinaryAllowPrivate = true
	cfg.Server.ExternalBinaryProxy = ""
	cfg.Server.ExternalBinaryProxyHosts = nil
	cfg.Server.ExternalBinaryTTL = "24h"
	sc := &svc.ServiceContext{Config: cfg}

	// 已上传离线文件
	local := externalBinaryLocalPath(dir, extbin.Entry{Name: "local-script.sh", Script: true})
	os.MkdirAll(filepath.Dir(local), 0755)
	os.WriteFile(local, []byte("#!/bin/sh\n"), 0644)
	// 已有未过期缓存
	cached := externalBinaryCachePath(dir, extbin.Entry{Name: "cached-script.sh", Script: true})
	os.MkdirAll(filepath.Dir(cached), 0755)
	os.WriteFile(cached, []byte("#!/bin/sh\n"), 0644)

	if n := prewarmExternalBinaries(context.Background(), sc); n != 1 {
		t.Fatalf("应只下载 1 个条目，实际 %d", n)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("上游访问次数 = %d, want 1", got)
	}
	target := externalBinaryCachePath(dir, extbin.Entry{Name: "fresh-script.sh", Script: true})
	if !fileExists(target) {
		t.Fatal("预热后应生成缓存文件")
	}

	// 第二轮：缓存未过期，不再下载。
	if n := prewarmExternalBinaries(context.Background(), sc); n != 0 {
		t.Fatalf("缓存未过期时不应重复下载，实际 %d", n)
	}
}

// TestBuiltinEntryAlwaysServed 早期保存过白名单（不含 node-exporter-install.sh）的部署，
// 访问内置条目不再 404：local=1 有缓存即返回。
func TestBuiltinEntryAlwaysServed(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.AgentBinaryDir = dir
	cfg.Server.ExternalBinaries = map[string]string{
		"windows-exporter-amd64": extbin.DefaultBinaries()["windows-exporter-amd64"],
	}
	sc := &svc.ServiceContext{Config: cfg}

	cache := externalBinaryCachePath(dir, extbin.Entry{Name: "node-exporter-install.sh", Script: true})
	os.MkdirAll(filepath.Dir(cache), 0755)
	os.WriteFile(cache, []byte("#!/bin/bash\n"), 0644)

	req := httptest.NewRequest("GET", "/api/external/binary/node-exporter-install.sh?local=1", nil)
	req.SetPathValue("name", "node-exporter-install.sh")
	rec := httptest.NewRecorder()
	ExternalBinaryDownload(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("内置条目应可访问，实际 %d: %s", rec.Code, rec.Body.String())
	}
}
