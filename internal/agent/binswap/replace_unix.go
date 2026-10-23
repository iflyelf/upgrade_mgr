//go:build !windows

package binswap

import "os"

// replaceFile Unix 上 rename 覆盖是原子操作，目标正在运行时也可替换（inode 换血）。
func replaceFile(src, dst string) error {
	return os.Rename(src, dst)
}

// removeFile 删除目标文件；不存在时返回 os.ErrNotExist。
func removeFile(path string) error {
	return os.Remove(path)
}

// CleanupOld Unix 无需清理替换残留（rename 直接换 inode），空实现。
func CleanupOld(path string) {}
