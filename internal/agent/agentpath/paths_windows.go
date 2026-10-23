//go:build windows

package agentpath

import (
	"os"
	"path/filepath"
)

// dataDir 返回 %ProgramData%\upgrade-agent。
//
// Windows Server 2008 R2 起 ProgramData 均存在；环境变量缺失时回退到
// 系统盘默认位置，避免用相对路径把凭据写进当前工作目录。
func dataDir() string {
	if v := os.Getenv("ProgramData"); v != "" {
		return filepath.Join(v, "upgrade-agent")
	}
	return `C:\ProgramData\upgrade-agent`
}

// BinPath 默认二进制安装路径（Program Files 需要管理员权限，安装器本身以管理员运行）。
func BinPath() string {
	if v := os.Getenv("ProgramFiles"); v != "" {
		return filepath.Join(v, "upgrade-agent", "upgrade-agent.exe")
	}
	return `C:\Program Files\upgrade-agent\upgrade-agent.exe`
}

// StatePath 默认状态文件路径。ProgramData 默认 ACL 仅管理员与 SYSTEM 可写。
func StatePath() string { return filepath.Join(dataDir(), "state.json") }

// DataDir 默认数据目录。
func DataDir() string { return dataDir() }

// BackupDir 默认自升级备份目录。
func BackupDir() string { return filepath.Join(dataDir(), "backup") }

// LockDir 看门狗锁文件所在目录。
func LockDir() string { return dataDir() }
