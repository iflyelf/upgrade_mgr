package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

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
