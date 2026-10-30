//go:build windows

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
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/iflyelf/upgrade_mgr/internal/agent/svcmgr"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// TestWindowsUninstallSkipsStartWhenMarkedForDelete 真实 SCM + PowerShell 卸载脚本：
//
// 测试进程持有服务句柄，卸载脚本执行 sc.exe delete 后服务停在「已标记删除」，
// 此时 SCM 仍能打开该服务（Exists=true），旧逻辑会去启服并报 ERROR_SERVICE_MARKED_FOR_DELETE。
// 期望：执行器识别卸载动作，跳过启服，任务成功。
func TestWindowsUninstallSkipsStartWhenMarkedForDelete(t *testing.T) {
	requirePowerShell(t)
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("需要管理员权限才能注册测试服务，跳过")
	}
	m, err := mgr.Connect()
	if err != nil {
		t.Skipf("连接 SCM 失败，跳过: %v", err)
	}
	defer m.Disconnect()

	name := fmt.Sprintf("um-test-uninstall-%d", time.Now().UnixNano())
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	s, err := m.CreateService(name, exe, mgr.Config{StartType: mgr.StartManual, DisplayName: name})
	if err != nil {
		t.Fatalf("注册测试服务失败: %v", err)
	}
	// 持有句柄直到测试结束：sc.exe delete 只能把服务标记删除。
	defer func() {
		_ = s.Delete()
		_ = s.Close()
	}()

	// 与 install.ps1 一致：位置参数为动作，-Yes 为非交互开关（未声明会报「找不到参数」）。
	script := "param([string]$Action, [switch]$Yes)\r\n" +
		"Write-Output ('action=' + $Action)\r\n" +
		"& sc.exe delete '" + name + "'\r\n" +
		"exit $LASTEXITCODE\r\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(script))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cmd := protocol.Command{Payload: protocol.CommandPayload{
		ToVersion: "uninstall",
		Component: protocol.ComponentSpec{
			Name:        "windows_exporter_script_uninstall",
			DeployMode:  protocol.DeployScript,
			BackupDir:   filepath.Join(dir, "backup"),
			ServiceName: name,
			ScriptURL:   srv.URL + "/install.ps1",
			ExecArgs:    []string{"uninstall", "-Yes"},
			ExecTimeout: "2m",
		},
	}}

	res, err := Execute(context.Background(), cmd, svcmgr.New(""), "", nil)
	if err != nil || !res.Success {
		t.Fatalf("卸载应成功: err=%v log=\n%s", err, res.Log)
	}
	if !strings.Contains(res.Log, "action=uninstall") {
		t.Fatalf("卸载脚本未执行或参数未透传: %s", res.Log)
	}
	if !strings.Contains(res.Log, "已被卸载脚本删除，跳过启动") {
		t.Fatalf("应识别为已标记删除并跳过启服: %s", res.Log)
	}
	if strings.Contains(res.Log, "启动服务 "+name) {
		t.Fatalf("卸载后不应启动服务: %s", res.Log)
	}
}
