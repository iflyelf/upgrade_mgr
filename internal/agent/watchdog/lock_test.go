//go:build !windows

package watchdog

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestLockPathNotWorldWritable 锁目录不得位于 world-writable 路径，
// 且创建后权限必须收紧到 0700，防止非特权用户抢占锁禁用保活。
func TestLockPathNotWorldWritable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需 root 才能写入 /var/lib")
	}
	p, err := ensureLockPath()
	if err != nil {
		t.Fatalf("ensureLockPath: %v", err)
	}
	if strings.HasPrefix(p, "/var/lock/") || strings.HasPrefix(p, "/tmp/") {
		t.Fatalf("锁文件位于 world-writable 目录: %s", p)
	}
	fi, err := os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0022 != 0 {
		t.Fatalf("锁目录权限过宽: %v", fi.Mode().Perm())
	}
	t.Logf("✅ 锁路径安全: %s (dir mode %v)", p, fi.Mode().Perm())
}

// TestLockDirPermTightened 已存在的过宽目录会被主动收紧。
func TestLockDirPermTightened(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需 root")
	}
	// 故意放宽，再调用应被收紧
	if err := os.MkdirAll(lockDir, 0777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(lockDir, 0777)
	if _, err := ensureLockPath(); err != nil {
		t.Fatalf("ensureLockPath: %v", err)
	}
	fi, _ := os.Stat(lockDir)
	if fi.Mode().Perm()&0022 != 0 {
		t.Fatalf("未收紧过宽权限: %v", fi.Mode().Perm())
	}
	t.Logf("✅ 过宽目录已被收紧为 %v", fi.Mode().Perm())
}

// TestLockOpenRejectsSymlink O_NOFOLLOW 阻断把锁文件替换为符号链接的攻击。
func TestLockOpenRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "watchdog.lock")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	_, err := os.OpenFile(link, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err == nil {
		t.Fatal("O_NOFOLLOW 未阻断符号链接")
	}
	t.Logf("✅ 符号链接被阻断: %v", err)
}
