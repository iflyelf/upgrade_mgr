package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestAgentBinaryDownloadArchitectures 验证 arm64 / arm64-legacy / Windows arm64
// 都能从 server.agent_binary_dir 正确下发，旧内核自动重定向到 legacy。
func TestAgentBinaryDownloadArchitectures(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"upgrade-agent-linux-amd64":        "linux-amd64",
		"upgrade-agent-linux-arm64":        "linux-arm64",
		"upgrade-agent-linux-arm64-legacy": "linux-arm64-legacy",
		"upgrade-agent-windows-arm64.exe":  "windows-arm64",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Server.AgentBinaryDir = dir
	sc := &svc.ServiceContext{Config: cfg}

	call := func(name, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/agent/binary/"+name+query, nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		AgentBinaryDownload(sc)(rec, req)
		return rec
	}

	for _, name := range []string{"linux-arm64", "linux-arm64-legacy", "windows-arm64"} {
		rec := call(name, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 应 200，实际 %d", name, rec.Code)
		}
		wantName := "upgrade-agent-" + name
		if name == "windows-arm64" {
			wantName = "upgrade-agent-windows-arm64.exe"
		}
		if rec.Body.String() != files[wantName] {
			t.Fatalf("%s 内容不符: %q", name, rec.Body.String())
		}
	}

	// 2.x 内核请求普通 arm64 时自动重定向到 arm64-legacy，并保留 kernel 参数。
	rec := call("linux-arm64", "?kernel=2.6.32")
	if rec.Code != http.StatusFound {
		t.Fatalf("旧内核应重定向，实际 %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "linux-arm64-legacy") || !strings.Contains(loc, "kernel=2.6.32") {
		t.Fatalf("重定向地址不符: %s", loc)
	}

	if rec := call("linux-mips", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("非法架构应 404，实际 %d", rec.Code)
	}
	if rec := call("linux-arm64-missing", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("非法文件名应 404，实际 %d", rec.Code)
	}
}
