//go:build !windows

package executor

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// newScriptCmd 构造 script 模式指令：制品内容就是安装脚本。
// InstallPath 故意留空，用来验证 script 模式不依赖平台备份/替换路径。
func newScriptCmd(t *testing.T, script string) protocol.Command {
	t.Helper()
	payload := []byte(script)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	sum := md5.Sum(payload)
	dir := t.TempDir()
	return protocol.Command{Payload: protocol.CommandPayload{
		ToVersion: "latest",
		Component: protocol.ComponentSpec{
			Name:        "node_exporter",
			DeployMode:  protocol.DeployScript,
			BackupDir:   filepath.Join(dir, "backup"),
			ExecTimeout: "30s",
		},
		Artifact: protocol.Artifact{URL: srv.URL + "/install.sh", MD5: hex.EncodeToString(sum[:])},
	}}
}

func TestExecute_ScriptMode(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "installed")
	cmd := newScriptCmd(t, "#!/bin/sh\ntouch \"$1\"\n")
	cmd.Payload.Component.ExecArgs = []string{marker}

	res, err := Execute(context.Background(), cmd, &fakeMgr{}, "", nil)
	if err != nil || !res.Success {
		t.Fatalf("script 模式应成功: err=%v log=\n%s", err, res.Log)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("安装脚本未执行: %v", err)
	}
	if res.BackupPath != "" {
		t.Fatalf("script 模式不应产生平台备份: %q", res.BackupPath)
	}
	if !strings.Contains(res.Log, "执行安装脚本") {
		t.Fatalf("日志缺少脚本执行记录:\n%s", res.Log)
	}
}

func TestExecute_ScriptModeFailure(t *testing.T) {
	cmd := newScriptCmd(t, "#!/bin/sh\necho boom >&2\nexit 7\n")

	res, err := Execute(context.Background(), cmd, &fakeMgr{}, "", nil)
	if err == nil || res.Success {
		t.Fatalf("脚本非零退出应失败: err=%v log=\n%s", err, res.Log)
	}
	if !strings.Contains(res.Log, "安装脚本执行失败") {
		t.Fatalf("日志缺少失败记录:\n%s", res.Log)
	}
}
