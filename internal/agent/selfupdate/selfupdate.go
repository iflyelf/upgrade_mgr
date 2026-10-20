// Package selfupdate Agent 自升级：exit 75 触发 + 崩溃回滚。
//
// 设计：Agent 收到 self_update 指令 → 下载新版本 → 备份当前二进制 → 替换 → exit(75)
// → systemd/upstart 重启 Agent → 新版本启动上报成功 / 崩溃多次则 watchdog 回滚旧版本。
package selfupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/agent/binswap"
	"github.com/iflyelf/upgrade_mgr/internal/agent/downloader"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

const (
	ExitCodeSelfUpdate = 75 // systemd ExecStartPre 可捕获此退出码触发自升级
)

// Execute 执行自升级：下载→备份→替换→exit(75)。
func Execute(ctx context.Context, cmd protocol.Command, binPath, authHeader string) error {
	art := cmd.Payload.Artifact
	// 安全：PID 可预测，攻击者可预先在 /tmp 放置同名符号链接，使 RemoveAll 删除任意路径。
	// 改用 MkdirTemp：名称含内核随机数且以 0700 创建，已存在则失败，杜绝抢占与符号链接攻击。
	tmpDir, err := os.MkdirTemp("", "upgrade-agent-selfupdate-")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dlPath := filepath.Join(tmpDir, "upgrade-agent.new")
	if err := downloader.Download(ctx, downloader.Spec{
		// 安全：自升级替换的是以 root 运行的 Agent 自身，必须优先用 SHA256 强校验；
		// MD5 已可碰撞，仅作旧制品兼容（downloader 优先 SHA256）。
		URL: art.URL, Dest: dlPath, MD5: art.MD5, SHA256: art.SHA256,
		AuthHeader: authHeader, MaxRetries: 3, Timeout: 10 * time.Minute,
	}); err != nil {
		return fmt.Errorf("下载新版本失败: %w", err)
	}

	backupDir := "/var/lib/upgrade-agent/backup"
	// 安全：备份目录若可被非特权用户写入，攻击者可植入恶意 .bak 文件，
	// 待 watchdog 触发回滚时以 root 身份执行，形成提权。此处强制收紧为 0700。
	if err := hardenDir(backupDir); err != nil {
		return fmt.Errorf("备份目录不安全: %w", err)
	}
	backup, err := binswap.Backup(binPath, backupDir)
	if err != nil {
		return fmt.Errorf("备份当前版本失败: %w", err)
	}
	fmt.Printf("已备份当前版本到: %s\n", backup)

	if err := binswap.Replace(dlPath, binPath); err != nil {
		return fmt.Errorf("替换失败: %w", err)
	}

	fmt.Printf("✅ 自升级准备完成，退出码 75 触发重启...\n")
	os.Exit(ExitCodeSelfUpdate)
	return nil // 不会执行到这里
}

// Rollback 回滚到最近备份（由 watchdog 在新版本持续崩溃时调用）。
//
// 安全要点：回滚会把备份文件提升为以 root 运行的 Agent 二进制，
// 因此必须确认备份目录与备份文件不可被非特权用户写入，否则构成提权路径。
func Rollback(binPath, backupDir string) error {
	if err := ensureSecureDir(backupDir); err != nil {
		return fmt.Errorf("备份目录不安全，拒绝回滚: %w", err)
	}
	// 只查找 upgrade-agent 的备份，避免匹配到组件二进制的备份
	backupBase := filepath.Base(binPath) // 如 "upgrade-agent"
	backups, err := filepath.Glob(filepath.Join(backupDir, backupBase+".bak.*"))
	if err != nil || len(backups) == 0 {
		return fmt.Errorf("未找到备份")
	}
	// 按时间戳数值取最新：字典序在时间戳位数变化时会出错，故显式解析数值。
	latest, latestTS := "", int64(-1)
	for _, b := range backups {
		idx := strings.LastIndex(b, ".bak.")
		if idx < 0 {
			continue
		}
		ts, convErr := strconv.ParseInt(b[idx+len(".bak."):], 10, 64)
		if convErr != nil {
			continue
		}
		if ts > latestTS {
			latest, latestTS = b, ts
		}
	}
	if latest == "" {
		return fmt.Errorf("未找到合法备份")
	}
	if err := ensureSecureFile(latest); err != nil {
		return fmt.Errorf("备份文件不安全，拒绝回滚: %w", err)
	}
	fmt.Printf("回滚到备份: %s\n", latest)
	return binswap.Rollback(latest, binPath)
}

// hardenDir 创建目录并收紧权限为 0700（仅属主可写）。
func hardenDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	return ensureSecureDir(dir)
}

// ensureSecureDir 校验目录不可被同组/其他用户写入。
func ensureSecureDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s 不是目录", dir)
	}
	if mode := fi.Mode().Perm(); mode&0022 != 0 {
		return fmt.Errorf("目录 %s 权限过宽 (%o)，可被非属主写入", dir, mode)
	}
	return nil
}

// ensureSecureFile 校验文件为常规文件（非符号链接）且不可被同组/其他用户写入。
func ensureSecureFile(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s 是符号链接", path)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s 不是常规文件", path)
	}
	if mode := fi.Mode().Perm(); mode&0022 != 0 {
		return fmt.Errorf("文件 %s 权限过宽 (%o)，可被非属主写入", path, mode)
	}
	return nil
}
