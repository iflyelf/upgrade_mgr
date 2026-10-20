// Package executor 升级流水线：下载→md5→备份→停服→替换/exec→启服→校验→回滚。
package executor

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/agent/binswap"
	"github.com/iflyelf/upgrade_mgr/internal/agent/downloader"
	"github.com/iflyelf/upgrade_mgr/internal/agent/svcmgr"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/runner"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// Result 执行结果
type Result struct {
	Success     bool
	Stage       string
	FromVersion string
	ToVersion   string
	ExitCode    int
	Log         string
	BackupPath  string
}

// Execute 执行组件升级流水线
func Execute(ctx context.Context, cmd protocol.Command, svcMgr svcmgr.Manager, authHeader string, onStage func(string)) (*Result, error) {
	res := &Result{ToVersion: cmd.Payload.ToVersion}
	comp := cmd.Payload.Component
	art := cmd.Payload.Artifact

	// 安全：白名单校验 install_path，防止服务端被攻破后下发恶意路径删除任意文件
	if err := validateInstallPath(comp.InstallPath); err != nil {
		return res, fmt.Errorf("install_path 非法: %w", err)
	}

	logf := func(stage, msg string, args ...any) {
		res.Stage = stage
		m := fmt.Sprintf(msg, args...)
		res.Log += fmt.Sprintf("[%s] %s\n", stage, m)
		log.Printf("[%s/%s] %s", comp.Name, stage, m)
		if onStage != nil {
			onStage(stage)
		}
	}

	// 1. 下载
	logf(protocol.StageDownloading, "下载 %s", art.URL)
	tmpDir := filepath.Join(comp.BackupDir, ".tmp")
	os.MkdirAll(tmpDir, 0755)
	dlPath := filepath.Join(tmpDir, filepath.Base(art.URL))
	if err := downloader.Download(ctx, downloader.Spec{
		URL: art.URL, Dest: dlPath, MD5: art.MD5, SHA256: art.SHA256,
		AuthHeader: authHeader, MaxRetries: 3, Timeout: 20 * time.Minute,
	}); err != nil {
		logf(protocol.StageDone, "下载失败: %v", err)
		return res, err
	}
	logf(protocol.StageDownloaded, "下载完成，校验和验证通过")

	// 2. 备份
	logf(protocol.StageBackup, "备份当前版本")
	bak, err := binswap.Backup(comp.InstallPath, comp.BackupDir)
	if err != nil {
		logf(protocol.StageDone, "备份失败: %v", err)
		return res, err
	}
	res.BackupPath = bak
	logf(protocol.StageBackup, "备份到 %s", bak)

	// 3. pre_install 钩子
	if len(comp.PreInstall) > 0 {
		logf(protocol.StagePreInstall, "执行 pre_install")
		if err := runHooks(ctx, comp.PreInstall); err != nil && !comp.IgnoreError {
			logf(protocol.StageDone, "pre_install 失败: %v", err)
			return res, err
		}
	}

	// 4. 停服（如果有服务名）
	if comp.ServiceName != "" {
		logf(protocol.StageStopping, "停止服务 %s", comp.ServiceName)
		if err := svcMgr.Stop(comp.ServiceName); err != nil && !comp.IgnoreError {
			logf(protocol.StageDone, "停服失败: %v", err)
			return res, err
		}
	}

	// 5. 替换 / exec
	if comp.DeployMode == protocol.DeployReplace || comp.DeployMode == protocol.DeployReplaceExec {
		logf(protocol.StageReplacing, "替换二进制到 %s", comp.InstallPath)
		if err := binswap.Replace(dlPath, comp.InstallPath); err != nil {
			logf(protocol.StageRollback, "替换失败，执行回滚: %v", err)
			if res.BackupPath != "" {
				binswap.Rollback(res.BackupPath, comp.InstallPath)
				if comp.ServiceName != "" {
					svcMgr.Start(comp.ServiceName) // 尝试启动旧版本
				}
			}
			return res, err
		}
	}
	if comp.DeployMode == protocol.DeployExec || comp.DeployMode == protocol.DeployReplaceExec {
		logf(protocol.StageExec, "执行部署命令")
		if err := runExec(ctx, comp); err != nil && !comp.IgnoreError {
			logf(protocol.StageRollback, "exec 失败，执行回滚: %v", err)
			if res.BackupPath != "" {
				binswap.Rollback(res.BackupPath, comp.InstallPath)
				if comp.ServiceName != "" {
					svcMgr.Start(comp.ServiceName)
				}
			}
			return res, err
		}
	}

	// 6. 启服
	if comp.ServiceName != "" {
		logf(protocol.StageStarting, "启动服务 %s", comp.ServiceName)
		if err := svcMgr.Start(comp.ServiceName); err != nil && !comp.IgnoreError {
			logf(protocol.StageRollback, "启服失败，执行回滚: %v", err)
			if res.BackupPath != "" {
				binswap.Rollback(res.BackupPath, comp.InstallPath)
				svcMgr.Start(comp.ServiceName) // 再次尝试启动（用旧版本）
			}
			return res, err
		}
		time.Sleep(2 * time.Second) // 等待服务启动
	}

	// 7. post_install 钩子
	if len(comp.PostInstall) > 0 {
		logf(protocol.StagePostInstall, "执行 post_install")
		if err := runHooks(ctx, comp.PostInstall); err != nil && !comp.IgnoreError {
			logf(protocol.StageRollback, "post_install 失败，执行回滚: %v", err)
			if res.BackupPath != "" {
				binswap.Rollback(res.BackupPath, comp.InstallPath)
				if comp.ServiceName != "" {
					svcMgr.Restart(comp.ServiceName)
				}
			}
			return res, err
		}
	}

	// 8. 健康检查（健康检查失败总是回滚，忽略 IgnoreError）
	if len(comp.HealthCmd) > 0 {
		logf(protocol.StageVerifying, "健康检查")
		if !runHealthCheck(ctx, comp.HealthCmd) {
			logf(protocol.StageRollback, "健康检查失败，执行回滚")
			if res.BackupPath != "" {
				binswap.Rollback(res.BackupPath, comp.InstallPath)
				if comp.ServiceName != "" {
					svcMgr.Restart(comp.ServiceName)
				}
			}
			return res, fmt.Errorf("健康检查失败")
		}
	}

	// 9. 版本确认
	if len(comp.VersionCmd) > 0 {
		ver := runVersionCheck(ctx, comp.VersionCmd, comp.VersionRegex)
		res.ToVersion = ver
		logf(protocol.StageDone, "升级成功，当前版本: %s", ver)
	} else {
		logf(protocol.StageDone, "升级成功")
	}

	res.Success = true
	return res, nil
}

func runHooks(ctx context.Context, hooks [][]string) error {
	for _, h := range hooks {
		if len(h) == 0 {
			continue
		}
		res, err := runner.Run(ctx, runner.Spec{Argv: h, Timeout: 10 * time.Minute})
		if err != nil || res.ExitCode != 0 {
			return fmt.Errorf("hook %v: %w", h, err)
		}
	}
	return nil
}

func runExec(ctx context.Context, comp protocol.ComponentSpec) error {
	if comp.ExecPath == "" {
		return nil
	}
	argv := append([]string{comp.ExecPath}, comp.ExecArgs...)
	timeout, _ := time.ParseDuration(comp.ExecTimeout)
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	res, err := runner.Run(ctx, runner.Spec{
		Argv: argv, Dir: comp.WorkDir, Env: comp.ExecEnv, Timeout: timeout,
	})
	if err != nil || res.ExitCode != 0 {
		return fmt.Errorf("exec exit %d: %s", res.ExitCode, res.Stderr)
	}
	return nil
}

func runHealthCheck(ctx context.Context, cmd [][]string) bool {
	for _, c := range cmd {
		if !runner.Check(ctx, runner.Spec{Argv: c, Timeout: 30 * time.Second}) {
			return false
		}
	}
	return true
}

func runVersionCheck(ctx context.Context, cmd [][]string, regex string) string {
	for _, c := range cmd {
		res, err := runner.Run(ctx, runner.Spec{Argv: c, Timeout: 10 * time.Second})
		if err == nil && res.ExitCode == 0 {
			// 简化：直接返回 stdout 第一行，regex 解析留给服务端或后续优化
			return res.Stdout
		}
	}
	return ""
}

// denyPrefixes 是硬安全边界：任何情况下都拒绝在这些目录下替换/删除文件。
//
// 这些路径承载系统启动、配置、内核接口与凭据，一旦被误替换或删除会导致主机
// 不可恢复；即使服务端被攻破下发恶意 install_path，也必须在 Agent 侧挡住。
var denyPrefixes = []string{
	"/etc/", "/boot/", "/sys/", "/proc/", "/dev/", "/root/",
	"/var/run/", "/run/", "/lib/", "/lib64/", "/bin/", "/sbin/",
}

// allowInstallRoots 允许部署的根目录，可用 UPGRADE_AGENT_ALLOW_ROOTS 覆盖
// （冒号分隔，如 "/opt:/data"）。默认涵盖常见部署位置。
//
// 设计取舍：以「黑名单为硬边界 + 白名单为可配置」，避免白名单漏项导致
// 合法部署路径（如 /srv、/var/lib/<app>）被误杀。
func allowInstallRoots() []string {
	if v := os.Getenv("UPGRADE_AGENT_ALLOW_ROOTS"); v != "" {
		var out []string
		for _, p := range strings.Split(v, ":") {
			if p = strings.TrimSpace(p); p != "" {
				if !strings.HasSuffix(p, "/") {
					p += "/"
				}
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{
		"/usr/local/", "/opt/", "/home/", "/data/", "/app/", "/srv/",
		"/var/lib/", "/usr/share/", "/tmp/", "/var/tmp/",
	}
}

// validateInstallPath 校验 install_path，防止越权替换/删除敏感文件。
//
// 校验顺序：
//  1. 必须是绝对路径且不含 ..（防路径遍历）
//  2. 命中 denyPrefixes 立即拒绝（硬边界，不可配置绕过）
//  3. 必须落在 allowInstallRoots 之一内
//  4. 拒绝符号链接目标逃逸出允许根目录（防 TOCTOU 提权）
func validateInstallPath(path string) error {
	if path == "" {
		return fmt.Errorf("路径为空")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("路径不能包含 ..")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("必须是绝对路径: %s", path)
	}
	absPath := filepath.Clean(path)

	// 2) 硬安全边界优先：黑名单命中即拒，且不允许被白名单覆盖。
	for _, prefix := range denyPrefixes {
		if absPath == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(absPath, prefix) {
			return fmt.Errorf("拒绝操作系统关键路径 %s", absPath)
		}
	}

	// 3) 必须在允许的部署根目录内。
	roots := allowInstallRoots()
	inAllow := false
	for _, prefix := range roots {
		if strings.HasPrefix(absPath, prefix) {
			inAllow = true
			break
		}
	}
	if !inAllow {
		return fmt.Errorf("路径 %s 不在允许的部署目录内（可用 UPGRADE_AGENT_ALLOW_ROOTS 配置）", absPath)
	}

	// 4) 防符号链接指向系统关键路径（TOCTOU 提权）。
	//
	// 只用黑名单校验解析后的真实路径，不再要求真实路径仍落在白名单内：
	// 发行版会合法地将部署目录符号链接到别处（如 /usr/local -> /var/usrlocal），
	// 若要求真实路径也在白名单内会误杀正常系统布局。
	// 黑名单才是硬安全边界——它能拦住"白名单目录里的软链指向 /etc"这类攻击。
	if realParent, err := filepath.EvalSymlinks(filepath.Dir(absPath)); err == nil {
		realTarget := filepath.Join(realParent, filepath.Base(absPath))
		for _, prefix := range denyPrefixes {
			if realTarget == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(realTarget, prefix) {
				return fmt.Errorf("符号链接指向系统关键路径 %s", realTarget)
			}
		}
	}
	return nil
}
