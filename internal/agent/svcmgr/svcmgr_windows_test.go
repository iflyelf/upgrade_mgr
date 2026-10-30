//go:build windows

package svcmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// createTestService 注册一个手动启动的临时服务（只注册不启动），返回持有的句柄。
//
// 需要管理员权限：GitHub windows-latest runner 默认是管理员；
// 自托管 runner 非管理员时跳过。
func createTestService(t *testing.T, name string) *mgr.Service {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("需要管理员权限才能注册测试服务，跳过")
	}
	m, err := mgr.Connect()
	if err != nil {
		t.Skipf("连接 SCM 失败，跳过: %v", err)
	}
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	s, err := m.CreateService(name, exe, mgr.Config{StartType: mgr.StartManual, DisplayName: name})
	if err != nil {
		m.Disconnect()
		t.Fatalf("注册测试服务失败: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Delete() // 已删除时返回错误，忽略
		_ = s.Close()
		_ = m.Disconnect()
	})
	return s
}

// TestWindowsMgrRemovedMarkedForDelete 真实 SCM：服务被 DeleteService 后若仍有句柄打开，
// 会停在「已标记删除」状态，Start 必然失败；Removed 必须识别为已删除。
func TestWindowsMgrRemovedMarkedForDelete(t *testing.T) {
	name := fmt.Sprintf("um-test-removed-%d", time.Now().UnixNano())
	s := createTestService(t, name)
	w := &windowsMgr{}

	if !w.Exists(name) {
		t.Fatal("刚注册的服务应存在")
	}
	if w.Removed(name) {
		t.Fatal("正常服务不应被判定为已删除")
	}

	// 测试持有句柄时删除：服务进入「已标记删除」，而不是立即消失。
	if err := s.Delete(); err != nil {
		t.Fatalf("删除服务失败: %v", err)
	}
	t.Logf("已标记删除时 Exists=%v（为 true 正是旧逻辑会去启服的原因）", w.Exists(name))
	if !w.Removed(name) {
		t.Fatal("已标记删除的服务应判定为已删除")
	}
	if err := w.Start(name); err == nil {
		t.Fatal("已标记删除的服务启动应失败（这就是卸载后不能启服的原因）")
	}

	// 关闭最后一个句柄后服务真正删除，Removed 仍为 true。
	_ = s.Close()
	deadline := time.Now().Add(10 * time.Second)
	for w.Exists(name) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if w.Exists(name) {
		t.Fatal("关闭句柄后服务应被删除")
	}
	if !w.Removed(name) {
		t.Fatal("已删除的服务应判定为已删除")
	}
}

// TestWindowsMgrRemovedNameChecks 不存在的服务算已删除；非法名称不判定为已删除（不需要管理员）。
func TestWindowsMgrRemovedNameChecks(t *testing.T) {
	w := &windowsMgr{}
	if !w.Removed(fmt.Sprintf("um-test-absent-%d", time.Now().UnixNano())) {
		t.Fatal("不存在的服务应判定为已删除")
	}
	for _, bad := range []string{"", "..", `a\b`, "-x"} {
		if w.Removed(bad) {
			t.Fatalf("非法名称 %q 不应判定为已删除", bad)
		}
	}
}
