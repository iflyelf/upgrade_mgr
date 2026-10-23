//go:build !windows

package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// script_url 应优先于 Artifact.URL，且没有校验和时也能下载执行。
func TestExecute_ScriptModeCustomURL(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "custom-url-installed")
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, "#!/bin/sh\ntouch \"$1\"\n")
	}))
	t.Cleanup(srv.Close)

	cmd := protocol.Command{Payload: protocol.CommandPayload{
		ToVersion: "latest",
		Component: protocol.ComponentSpec{
			Name:        "node_exporter",
			DeployMode:  protocol.DeployScript,
			BackupDir:   filepath.Join(t.TempDir(), "backup"),
			ExecTimeout: "30s",
			ScriptURL:   srv.URL + "/install.sh?token=abc",
			ExecArgs:    []string{marker},
		},
		// 自定义 URL 应覆盖制品地址；校验和为空时跳过校验。
		Artifact: protocol.Artifact{URL: "/api/artifacts/1/download"},
	}}

	res, err := Execute(context.Background(), cmd, &fakeMgr{}, "Bearer should-not-leak", nil)
	if err != nil || !res.Success {
		t.Fatalf("自定义 script_url 应执行成功: err=%v log=\n%s", err, res.Log)
	}
	if gotPath != "/install.sh?token=abc" {
		t.Fatalf("未从 script_url 下载，实际请求 path=%q", gotPath)
	}
	if gotAuth != "" {
		t.Fatalf("自定义 script_url 不应收到 Agent 凭据，got %q", gotAuth)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("自定义脚本未执行: %v", err)
	}
	if !strings.Contains(res.Log, "未提供校验和") {
		t.Fatalf("日志应提示跳过校验:\n%s", res.Log)
	}
}

func TestExecute_ScriptModeRejectsNonHTTPURL(t *testing.T) {
	cmd := protocol.Command{Payload: protocol.CommandPayload{
		Component: protocol.ComponentSpec{
			Name:       "node_exporter",
			DeployMode: protocol.DeployScript,
			BackupDir:  t.TempDir(),
			ScriptURL:  "file:///etc/passwd",
		},
		Artifact: protocol.Artifact{URL: "/api/artifacts/1/download"},
	}}
	_, err := Execute(context.Background(), cmd, &fakeMgr{}, "", nil)
	if err == nil || !strings.Contains(err.Error(), "script_url 非法") {
		t.Fatalf("file:// 应被拒绝，got %v", err)
	}
}

func TestRedactURL(t *testing.T) {
	got := redactURL("https://user:pass@example.com/install.sh?token=secret")
	if strings.Contains(got, "pass") || strings.Contains(got, "secret") {
		t.Fatalf("URL 凭据未脱敏: %q", got)
	}
	if !strings.Contains(got, "example.com/install.sh") {
		t.Fatalf("应保留主机和路径便于排查: %q", got)
	}
}

func TestArtifactFilename(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://example.com/install.sh", "install.sh"},
		{"https://example.com/install.sh?token=abc", "install.sh"},
		{"https://example.com/path/", "path"},
		{"https://example.com/api/artifacts/42/download", "artifact-42.bin"},
		{"https://example.com", "artifact"},
		{"https://example.com/..", "artifact"},
	}
	for _, tc := range cases {
		if got := artifactFilename(tc.raw); got != tc.want {
			t.Errorf("artifactFilename(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
