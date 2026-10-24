package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRollbackRejectsWorldWritableDir 备份目录可被他人写入时必须拒绝回滚（防提权）。
func TestRollbackRejectsWorldWritableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 使用 ACL，跳过 Unix 权限检查")
	}
	dir := t.TempDir()
	bk := filepath.Join(dir, "backup")
	if err := os.MkdirAll(bk, 0777); err != nil { // 故意过宽
		t.Fatal(err)
	}
	os.Chmod(bk, 0777)
	os.WriteFile(filepath.Join(bk, "agent.bak.100"), []byte("x"), 0600)

	err := Rollback(filepath.Join(dir, "agent"), bk)
	if err == nil {
		t.Fatal("过宽备份目录应拒绝回滚，实际放行（提权风险）")
	}
	t.Logf("✅ 过宽备份目录被拒: %v", err)
}

// TestRollbackRejectsSymlinkBackup 备份文件为符号链接时必须拒绝（防指向任意文件）。
func TestRollbackRejectsSymlinkBackup(t *testing.T) {
	dir := t.TempDir()
	bk := filepath.Join(dir, "backup")
	os.MkdirAll(bk, 0700)

	evil := filepath.Join(dir, "evil")
	os.WriteFile(evil, []byte("malicious"), 0600)
	if err := os.Symlink(evil, filepath.Join(bk, "agent.bak.100")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	err := Rollback(filepath.Join(dir, "agent"), bk)
	if err == nil {
		t.Fatal("符号链接备份应被拒绝")
	}
	t.Logf("✅ 符号链接备份被拒: %v", err)
}

// TestRollbackPicksNumericLatest 时间戳位数变化时，必须按数值而非字典序取最新。
func TestRollbackPicksNumericLatest(t *testing.T) {
	dir := t.TempDir()
	bk := filepath.Join(dir, "backup")
	os.MkdirAll(bk, 0700)
	target := filepath.Join(dir, "agent")
	os.WriteFile(target, []byte("current"), 0700)

	// 字典序: "9999999999" > "10000000000"，但数值相反
	os.WriteFile(filepath.Join(bk, "agent.bak.9999999999"), []byte("OLD"), 0600)
	os.WriteFile(filepath.Join(bk, "agent.bak.10000000000"), []byte("NEW"), 0600)

	if err := Rollback(target, bk); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "NEW" {
		t.Fatalf("应回滚到数值最新的备份 NEW，实际=%q（字典序 bug）", got)
	}
	t.Logf("✅ 按时间戳数值取最新备份: %s", got)
}

// TestEnsureSecureDirDetectsGroupWritable 同组可写也应被拒。
func TestEnsureSecureDirDetectsGroupWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 使用 ACL，跳过 Unix 权限检查")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "g")
	os.MkdirAll(sub, 0770)
	os.Chmod(sub, 0770)
	if err := ensureSecureDir(sub); err == nil {
		t.Fatal("同组可写目录应被拒绝")
	}
	os.Chmod(sub, 0700)
	if err := ensureSecureDir(sub); err != nil {
		t.Fatalf("0700 目录应通过: %v", err)
	}
	fmt.Fprintln(os.Stderr, "")
	t.Logf("✅ 同组可写被拒、0700 放行")
}
