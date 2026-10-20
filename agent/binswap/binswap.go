// Package binswap 实现二进制的备份、原子替换与回滚。
package binswap

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Backup 备份目标文件到 backupDir/filename.bak.timestamp
func Backup(target, backupDir string) (string, error) {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return "", nil // 目标不存在，无需备份
	}
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("创建备份目录失败: %w", err)
	}
	base := filepath.Base(target)
	backup := filepath.Join(backupDir, fmt.Sprintf("%s.bak.%d", base, time.Now().Unix()))
	if err := copyFile(target, backup); err != nil {
		return "", fmt.Errorf("备份失败: %w", err)
	}
	return backup, nil
}

// Replace 原子替换（rename）；src 可以是文件或目录。
// 替换前会设置可执行权限 0755。
func Replace(src, dst string) error {
	// 确保新文件有可执行权限
	if err := os.Chmod(src, 0755); err != nil {
		return fmt.Errorf("设置可执行权限失败: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("替换失败: %w", err)
	}
	return nil
}

// Rollback 从备份恢复到目标。
func Rollback(backup, target string) error {
	if backup == "" || !fileExists(backup) {
		return fmt.Errorf("备份文件不存在: %s", backup)
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除当前文件失败: %w", err)
	}
	if err := copyFile(backup, target); err != nil {
		return fmt.Errorf("回滚失败: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	return out.Sync()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
