//go:build windows

package binswap

import (
	"fmt"
	"os"
)

// oldSuffix 是 Windows 上被换下文件的临时后缀。
//
// Windows 不允许删除或覆盖正在运行的 exe，但允许重命名。因此替换策略为：
// 先把旧文件改名为 <dst>.old，再把新文件放到目标位置。残留的 .old
// 由 CleanupOld 在下次启动时删除（此时旧进程已退出）。
const oldSuffix = ".old"

// replaceFile Windows 替换：旧文件改名让位 → 新文件就位 → 尽力清理 .old。
func replaceFile(src, dst string) error {
	old := dst + oldSuffix
	_ = os.Remove(old) // 上次残留（旧进程已退出时才能删掉，忽略失败）

	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, old); err != nil {
			return fmt.Errorf("重命名旧文件失败（文件可能被占用）: %w", err)
		}
	}
	if err := os.Rename(src, dst); err != nil {
		// 回滚：把旧文件放回原位，避免目标位置空缺导致服务无法启动。
		if _, statErr := os.Stat(old); statErr == nil {
			_ = os.Rename(old, dst)
		}
		return err
	}
	// 正在运行的旧进程仍占用 .old 时删除会失败，忽略即可（启动时再清）。
	_ = os.Remove(old)
	return nil
}

// removeFile 删除目标文件；不存在时返回 os.ErrNotExist。
// 若文件正被运行中的进程占用，Windows 会标记为「删除挂起」，重启后消失。
func removeFile(path string) error {
	return os.Remove(path)
}

// CleanupOld 删除上次替换遗留的 <path>.old 文件。
//
// Agent 启动时调用：新版本已就位，旧进程已退出，此时可以安全清理。
func CleanupOld(path string) {
	_ = os.Remove(path + oldSuffix)
}
