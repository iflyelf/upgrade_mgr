package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// TestExecute_UninstallSkipsStart 验证卸载动作不启服（避免 Windows DELETE_PENDING / systemd 缓存未刷新导致失败）。
func TestExecute_UninstallSkipsStart(t *testing.T) {
	cmd := newCmd(t)
	cmd.Payload.Component.ExecArgs = []string{"uninstall", "-y"}
	cmd.Payload.Component.DeployMode = protocol.DeployExec
	cmd.Payload.Component.ScriptURL = cmd.Payload.Artifact.URL // exec + script_url
	cmd.Payload.Artifact = protocol.Artifact{}                 // 不下载制品

	// 模拟脚本删除服务：停服（含 Stop 内部的存在性检查）期间服务存在，
	// 第一次 Stop 返回之后视为脚本已删除服务。
	removed := false
	mgr := &fakeMgr{}
	mgr.existsFn = func() bool { return !removed }
	stop := &stopHookMgr{fakeMgr: mgr, after: func() { removed = true }}

	res, err := Execute(context.Background(), cmd, stop, "", nil)
	if err != nil || !res.Success {
		t.Fatalf("卸载应成功: err=%v log=\n%s", err, res.Log)
	}
	if mgr.stopCalls != 1 {
		t.Errorf("应调用一次 Stop: got %d", mgr.stopCalls)
	}
	if mgr.startCalls != 0 {
		t.Errorf("卸载后不应调用 Start: got %d", mgr.startCalls)
	}
	if !strings.Contains(res.Log, "已被卸载脚本删除，跳过启动") {
		t.Errorf("日志应说明跳过启动原因: %s", res.Log)
	}
}

// TestExecute_UninstallServiceStillExists 卸载后服务仍存在时只警告，不启服。
func TestExecute_UninstallServiceStillExists(t *testing.T) {
	cmd := newCmd(t)
	cmd.Payload.Component.ExecArgs = []string{"remove"}
	cmd.Payload.Component.DeployMode = protocol.DeployExec
	cmd.Payload.Component.ScriptURL = cmd.Payload.Artifact.URL
	cmd.Payload.Artifact = protocol.Artifact{}

	mgr := &fakeMgr{exists: true} // 脚本执行前后都存在（卸载脚本可能失败）
	res, err := Execute(context.Background(), cmd, mgr, "", nil)
	if err != nil || !res.Success {
		t.Fatalf("卸载应成功（即使服务未删除）: err=%v", err)
	}
	if mgr.startCalls != 0 {
		t.Errorf("即使服务存在，卸载后也不应启服: got %d", mgr.startCalls)
	}
	if !strings.Contains(res.Log, "警告：卸载后服务") && !strings.Contains(res.Log, "仍存在") {
		t.Errorf("日志应包含卸载后服务仍存在的警告: %s", res.Log)
	}
}

// TestExecute_UninstallNeverExisted 卸载前服务就不存在（可能已卸载过）。
func TestExecute_UninstallNeverExisted(t *testing.T) {
	cmd := newCmd(t)
	cmd.Payload.Component.ExecArgs = []string{"purge", "--force"}
	cmd.Payload.Component.DeployMode = protocol.DeployExec
	cmd.Payload.Component.ScriptURL = cmd.Payload.Artifact.URL
	cmd.Payload.Artifact = protocol.Artifact{}

	mgr := &fakeMgr{exists: false}
	res, err := Execute(context.Background(), cmd, mgr, "", nil)
	if err != nil || !res.Success {
		t.Fatalf("卸载应成功: err=%v", err)
	}
	if mgr.stopCalls != 0 || mgr.startCalls != 0 {
		t.Errorf("服务不存在时不应调用 Stop/Start: stop=%d start=%d", mgr.stopCalls, mgr.startCalls)
	}
	if !strings.Contains(res.Log, "卸载前就不存在") {
		t.Errorf("日志应说明服务本就不存在: %s", res.Log)
	}
}

// stopHookMgr 在 Stop 成功返回后执行 after，用于模拟「停服后脚本删除了服务」。
type stopHookMgr struct {
	*fakeMgr
	after func()
}

func (m *stopHookMgr) Stop(name string) error {
	err := m.fakeMgr.Stop(name)
	if m.after != nil {
		m.after()
	}
	return err
}
