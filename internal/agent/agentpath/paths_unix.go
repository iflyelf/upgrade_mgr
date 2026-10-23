//go:build !windows

// Package agentpath 提供 Agent 在各操作系统上的默认安装与运行路径。
package agentpath

// BinPath 默认二进制安装路径。
func BinPath() string { return "/usr/local/bin/upgrade-agent" }

// StatePath 默认状态文件路径（含 Agent 凭据，仅 root 可读）。
func StatePath() string { return "/var/lib/upgrade-agent/state.json" }

// DataDir 默认数据目录。
func DataDir() string { return "/var/lib/upgrade-agent" }

// BackupDir 默认自升级备份目录。
func BackupDir() string { return DataDir() + "/backup" }

// LockDir 看门狗锁文件所在目录（必须仅 root 可写）。
func LockDir() string { return DataDir() }
