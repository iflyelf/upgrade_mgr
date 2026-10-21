package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// post_install_script 应写入临时文件并执行成功，输出进入任务日志。
func TestExecute_PostInstallScript(t *testing.T) {
	cmd := newCmd(t)
	marker := filepath.Join(t.TempDir(), "post-install.txt")
	cmd.Payload.Component.ExecEnv = []string{"MARKER=" + marker}
	cmd.Payload.Component.PostInstallScript = "#!/bin/sh\nprintf ok > \"$MARKER\"\necho script-done\n"

	res, err := Execute(context.Background(), cmd, &fakeMgr{}, "", nil)
	if err != nil || !res.Success {
		t.Fatalf("post_install 脚本应执行成功: err=%v log=\n%s", err, res.Log)
	}
	b, err := os.ReadFile(marker)
	if err != nil || string(b) != "ok" {
		t.Fatalf("脚本未写出预期内容: content=%q err=%v", string(b), err)
	}
	if !strings.Contains(res.Log, "执行 post_install 脚本") || !strings.Contains(res.Log, "script-done") {
		t.Fatalf("日志缺少脚本执行记录或输出:\n%s", res.Log)
	}
}

// post_install_script 非零退出应使任务失败，并记录脚本输出便于排查。
func TestExecute_PostInstallScriptFailure(t *testing.T) {
	cmd := newCmd(t)
	cmd.Payload.Component.PostInstallScript = "#!/bin/sh\necho boom >&2\nexit 9\n"

	res, err := Execute(context.Background(), cmd, &fakeMgr{}, "", nil)
	if err == nil || res.Success {
		t.Fatalf("脚本非零退出应失败: err=%v log=\n%s", err, res.Log)
	}
	if !strings.Contains(res.Log, "post_install 脚本失败") || !strings.Contains(res.Log, "boom") {
		t.Fatalf("日志缺少失败原因或脚本输出:\n%s", res.Log)
	}
}
