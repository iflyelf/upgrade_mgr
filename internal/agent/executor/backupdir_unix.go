//go:build !windows

package executor

// resolveBackupDir 返回组件备份目录；Unix 上原样使用（历史行为，空值相对当前目录）。
func resolveBackupDir(dir string) string { return dir }
