//go:build windows

package executor

import (
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentpath"
)

// resolveBackupDir 返回组件备份目录。
//
// Windows 上组件表的默认值是 Linux 路径（/var/lib/upgrade-agent/backup），
// 直接使用会被解释成「当前盘符:\var\lib\...」。这里改为 ProgramData 下的
// Agent 备份目录；运维显式填写的盘符路径则原样尊重。
func resolveBackupDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" || strings.HasPrefix(dir, "/") {
		return agentpath.BackupDir()
	}
	return dir
}
