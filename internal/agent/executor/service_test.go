package executor

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// fakeMgr 模拟服务管理器：exists 为 false 时 Stop/Start 返回错误（与真实 systemctl 行为一致）。
type fakeMgr struct {
	exists     bool
	existsFn   func() bool
	stopCalls  int
	startCalls int
}

func (f *fakeMgr) Type() string { return "fake" }
func (f *fakeMgr) Exists(string) bool {
	if f.existsFn != nil {
		return f.existsFn()
	}
	return f.exists
}
func (f *fakeMgr) Stop(string) error {
	f.stopCalls++
	if !f.Exists("") {
		return fmt.Errorf("Unit not found")
	}
	return nil
}
func (f *fakeMgr) Start(string) error {
	f.startCalls++
	if !f.Exists("") {
		return fmt.Errorf("Unit not found")
	}
	return nil
}
func (f *fakeMgr) Restart(n string) error        { return f.Start(n) }
func (f *fakeMgr) Status(string) (string, error) { return "", nil }

func newCmd(t *testing.T) protocol.Command {
	t.Helper()
	payload := []byte("#!/bin/sh\necho ok\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	sum := md5.Sum(payload)
	dir := t.TempDir()
	return protocol.Command{Payload: protocol.CommandPayload{
		ToVersion: "1.0.0",
		Component: protocol.ComponentSpec{
			Name:        "demo",
			DeployMode:  protocol.DeployReplace,
			InstallPath: filepath.Join(dir, "demo"),
			BackupDir:   filepath.Join(dir, "backup"),
			ServiceName: "demo",
		},
		Artifact: protocol.Artifact{URL: srv.URL + "/demo", MD5: hex.EncodeToString(sum[:])},
	}}
}

func TestExecute_FirstInstallServiceMissing(t *testing.T) {
	cmd := newCmd(t)
	mgr := &fakeMgr{exists: false}
	res, err := Execute(context.Background(), cmd, mgr, "", nil)
	if err != nil || !res.Success {
		t.Fatalf("首次安装服务不存在时不应失败: err=%v log=\n%s", err, res.Log)
	}
	if mgr.stopCalls != 0 || mgr.startCalls != 0 {
		t.Errorf("服务不存在时不应调用 Stop/Start: stop=%d start=%d", mgr.stopCalls, mgr.startCalls)
	}
}

func TestExecute_ServiceExists(t *testing.T) {
	cmd := newCmd(t)
	mgr := &fakeMgr{exists: true}
	res, err := Execute(context.Background(), cmd, mgr, "", nil)
	if err != nil || !res.Success {
		t.Fatalf("升级应成功: err=%v log=\n%s", err, res.Log)
	}
	if mgr.stopCalls != 1 || mgr.startCalls != 1 {
		t.Errorf("服务存在时应停服、启服各一次: stop=%d start=%d", mgr.stopCalls, mgr.startCalls)
	}
}

// 服务在替换/exec 之后才注册（例如安装脚本写入 unit），应在启服阶段被启动。
func TestExecute_ServiceRegisteredDuringInstall(t *testing.T) {
	cmd := newCmd(t)
	// 第一次探测（停服前）返回 false，之后返回 true
	n := 0
	mgr := &fakeMgr{}
	mgr.existsFn = func() bool { n++; return n > 1 }
	res, err := Execute(context.Background(), cmd, mgr, "", nil)
	if err != nil || !res.Success {
		t.Fatalf("应成功: err=%v log=\n%s", err, res.Log)
	}
	if mgr.stopCalls != 0 || mgr.startCalls != 1 {
		t.Errorf("应跳过停服、执行启服: stop=%d start=%d", mgr.stopCalls, mgr.startCalls)
	}
}
