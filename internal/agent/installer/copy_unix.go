//go:build !windows

package installer

import (
	"os"
)

// copyExecutablePlatform 原子复制：写临时文件后 rename 覆盖。
func copyExecutablePlatform(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, in, 0755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
