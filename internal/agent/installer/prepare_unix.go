//go:build !windows

package installer

// prepareInstall Unix 无需预处理：rename 可覆盖运行中的二进制。
func prepareInstall(opts Options) {}
