//go:build windows

package installer

import (
	"fmt"
	"os"
	"time"
)

// copyExecutablePlatform 复制当前 exe 到目标路径。
//
// Windows 不允许覆盖正在运行的 exe，但允许改名。旧文件使用唯一 .old.<时间戳>
// 名称，避免上一次残留的 .old 被占用导致 rename 失败；并对 Access denied
// 做短暂重试（等待服务/看门狗进程退出或杀软释放句柄）。
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
		old := fmt.Sprintf("%s.old.%d", dst, time.Now().UnixNano())
		var renameErr error
		for i := 0; i < 60; i++ {
			renameErr = os.Rename(dst, old)
			if renameErr == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if renameErr != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("旧文件无法让位（请先停止 upgrade-agent 服务/看门狗后重试）: %w", renameErr)
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Rename(old, dst) // 回滚，避免目标位置空缺
			_ = os.Remove(tmp)
			return err
		}
		// 正在运行的旧进程仍占用时删除会失败，下次启动 CleanupOld 会清理。
		_ = os.Remove(old)
		return nil
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
