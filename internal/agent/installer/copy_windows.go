//go:build windows

package installer

import (
	"fmt"
	"os"
)

// copyExecutablePlatform 复制当前 exe 到目标路径。
//
// Windows 不允许覆盖正在运行的 exe，但允许改名：
// 目标已存在（旧版本服务仍在运行）时先改名为 .old 让位。
func copyExecutablePlatform(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, in, 0755); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		old := dst + ".old"
		_ = os.Remove(old)
		if err := os.Rename(dst, old); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("旧文件无法让位（请先停止 upgrade-agent 服务）: %w", err)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(dst + ".old")
	return nil
}
