//go:build windows

package selfupdate

// isWindows 用于跳过 POSIX 权限位校验（Windows ACL 由系统管理）。
func isWindows() bool { return true }
