package binswap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupReplaceRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mybin")
	backupDir := filepath.Join(dir, "backup")
	newVer := filepath.Join(dir, "newbin")

	// 原始文件
	if err := os.WriteFile(target, []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}
	// 新版本
	if err := os.WriteFile(newVer, []byte("v2"), 0755); err != nil {
		t.Fatal(err)
	}

	// 备份
	bak, err := Backup(target, backupDir)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if bak == "" {
		t.Fatal("backup path empty")
	}
	bakData, _ := os.ReadFile(bak)
	if string(bakData) != "v1" {
		t.Fatal("backup content wrong")
	}

	// 替换
	if err := Replace(newVer, target); err != nil {
		t.Fatalf("replace: %v", err)
	}
	cur, _ := os.ReadFile(target)
	if string(cur) != "v2" {
		t.Fatal("replace failed")
	}

	// 回滚
	if err := Rollback(bak, target); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	rolled, _ := os.ReadFile(target)
	if string(rolled) != "v1" {
		t.Fatal("rollback content wrong")
	}
}
