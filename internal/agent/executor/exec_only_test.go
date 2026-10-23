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

// TestExecute_ExecModeWithScriptURL 验证 exec + script_url：
// Agent 下载外置脚本后，把 exec_args 作为脚本参数执行，
// 无需在 exec_args 里塞整段 shell。组件页三个「只执行命令」示例依赖该行为。
func TestExecute_ExecModeWithScriptURL(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "script-url-action")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#!/bin/sh\ntouch \"$1\"\n")
	}))
	t.Cleanup(srv.Close)

	cmd := protocol.Command{Payload: protocol.CommandPayload{
		ToVersion: "latest",
		Component: protocol.ComponentSpec{
			Name:        "node_exporter_cmd",
			DeployMode:  protocol.DeployExec,
			BackupDir:   filepath.Join(t.TempDir(), "backup"),
			ExecTimeout: "30s",
			ScriptURL:   srv.URL + "/install.sh",
			ExecArgs:    []string{marker},
		},
	}}

	res, err := Execute(context.Background(), cmd, &fakeMgr{}, "", nil)
	if err != nil || !res.Success {
		t.Fatalf("exec+script_url 应成功: err=%v log=\n%s", err, res.Log)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("脚本未按 exec_args 执行: %v", err)
	}
	if !strings.Contains(res.Log, "执行远程脚本") {
		t.Fatalf("日志应记录执行远程脚本:\n%s", res.Log)
	}
	if res.BackupPath != "" {
		t.Fatalf("exec+script_url 不应产生平台备份: %q", res.BackupPath)
	}
	if !strings.Contains(res.Log, "跳过平台备份") {
		t.Fatalf("日志应说明跳过备份:\n%s", res.Log)
	}
}

// TestExecute_ExecOnlyWithoutArtifact 验证「只执行命令」模式：
// 没有制品、没有安装路径、没有服务名时也能直接运行 exec 命令，
// 不下载、不备份、不替换任何文件。组件页「填入示例：只执行命令」依赖该行为。
func TestExecute_ExecOnlyWithoutArtifact(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	cmd := protocol.Command{Payload: protocol.CommandPayload{
		ToVersion: "latest",
		Component: protocol.ComponentSpec{
			Name:        "node_exporter_cmd",
			DeployMode:  protocol.DeployExec,
			ExecPath:    "/bin/sh",
			ExecArgs:    []string{"-c", "touch \"" + marker + "\""},
			ExecTimeout: "30s",
			BackupDir:   filepath.Join(t.TempDir(), "backup"),
		},
	}}

	res, err := Execute(context.Background(), cmd, &fakeMgr{}, "", nil)
	if err != nil || !res.Success {
		t.Fatalf("exec-only 应成功: err=%v log=\n%s", err, res.Log)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("命令未执行: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("exec-only 不应产生备份: %q", res.BackupPath)
	}
	if !strings.Contains(res.Log, "跳过下载") {
		t.Fatalf("日志应说明跳过下载:\n%s", res.Log)
	}
	if !strings.Contains(res.Log, "跳过备份") {
		t.Fatalf("日志应说明跳过备份:\n%s", res.Log)
	}
}
