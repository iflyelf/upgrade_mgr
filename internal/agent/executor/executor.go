// Package executor 升级流水线：下载→md5→备份→停服→替换/exec/script→启服→校验→回滚。
package executor

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
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
func Execute(ctx context.Context, cmd protocol.Command, svcMgr svcmgr.Manager, authHeader string, onStage func(stage, message string)) (*Result, error) {
	res := &Result{ToVersion: cmd.Payload.ToVersion}
	comp := cmd.Payload.Component
	art := cmd.Payload.Artifact

	// 安全：白名单校验 install_path，防止服务端被攻破后下发恶意路径删除任意文件。
	// script 模式不读写 install_path（安装位置由脚本自行决定），因此不做该校验。
	if comp.DeployMode != protocol.DeployScript {
		if err := validateInstallPath(comp.InstallPath); err != nil {
			return res, fmt.Errorf("install_path 非法: %w", err)
		}
	}

	logf := func(stage, msg string, args ...any) {
		res.Stage = stage
		m := fmt.Sprintf(msg, args...)
		res.Log += fmt.Sprintf("[%s] %s\n", stage, m)
		log.Printf("[%s/%s] %s", comp.Name, stage, m)
		if onStage != nil {
			onStage(stage, m)
		}
	}

	// 1. 下载
	downloadURL := art.URL
	downloadAuth := authHeader
	if comp.DeployMode == protocol.DeployScript && comp.ScriptURL != "" {
		scriptURL := strings.TrimSpace(comp.ScriptURL)
		if err := validateScriptURL(scriptURL); err != nil {
			logf(protocol.StageFailed, "script_url 非法: %v", err)
			return res, fmt.Errorf("script_url 非法: %w", err)
		}
		// 以组件字段为准，避免同一指令里 Artifact.URL 与 ScriptURL 不一致。
		downloadURL = scriptURL
		// 自定义地址可能是第三方仓库，绝不携带平台 Agent 凭据。
		downloadAuth = ""
	}
	logf(protocol.StageDownloading, "下载 %s", redactURL(downloadURL))
	tmpDir := filepath.Join(comp.BackupDir, ".tmp")
	os.MkdirAll(tmpDir, 0755)
	dlPath := filepath.Join(tmpDir, artifactFilename(downloadURL))
	// 成功执行后清理临时下载文件：避免下次升级同名制品时残留旧文件，
	// 触发 Range 续传 HTTP 416（本地文件比远端大）而反复卡在下载。
	defer func() {
		if res.Success {
			_ = os.Remove(dlPath)
		}
	}()
	if err := downloader.Download(ctx, downloader.Spec{
		URL: downloadURL, Dest: dlPath, MD5: art.MD5, SHA256: art.SHA256,
		AuthHeader: downloadAuth, MaxRetries: 3, Timeout: 20 * time.Minute,
	}); err != nil {
		logf(protocol.StageFailed, "下载失败: %v", err)
		return res, err
	}
	if art.MD5 != "" || art.SHA256 != "" {
		logf(protocol.StageDownloaded, "下载完成，校验和验证通过")
	} else {
		logf(protocol.StageDownloaded, "下载完成（未提供校验和，跳过校验）")
	}

	// 2. 备份（script 模式由安装脚本自行处理升级/备份）
	if comp.DeployMode == protocol.DeployScript {
		logf(protocol.StageBackup, "脚本模式：跳过平台备份，由安装脚本自行处理")
	} else {
		logf(protocol.StageBackup, "备份当前版本")
		bak, err := binswap.Backup(comp.InstallPath, comp.BackupDir)
		if err != nil {
			logf(protocol.StageFailed, "备份失败: %v", err)
			return res, err
		}
		res.BackupPath = bak
		logf(protocol.StageBackup, "备份到 %s", bak)
	}

	// 3. pre_install 钩子
	if len(comp.PreInstall) > 0 {
		logf(protocol.StagePreInstall, "执行 pre_install")
		if err := runHooks(ctx, comp.PreInstall); err != nil && !comp.IgnoreError {
			logf(protocol.StageFailed, "pre_install 失败: %v", err)
			return res, err
		}
	}

	// svcExists 服务名已填且本机已注册该服务。首次安装时服务尚不存在，
	// 停服/回滚启服均应跳过而不是报错。
	svcExists := func() bool {
		return comp.ServiceName != "" && svcMgr.Exists(comp.ServiceName)
	}

	// 4. 停服（如果有服务名且服务已存在）
	if comp.ServiceName != "" {
		if !svcExists() {
			logf(protocol.StageStopping, "服务 %s 尚未安装，跳过停服（首次安装）", comp.ServiceName)
		} else {
			logf(protocol.StageStopping, "停止服务 %s", comp.ServiceName)
			if err := svcMgr.Stop(comp.ServiceName); err != nil && !comp.IgnoreError {
				logf(protocol.StageFailed, "停服失败: %v", err)
				return res, err
			}
		}
	}

	// 5. 替换 / exec
	if comp.DeployMode == protocol.DeployReplace || comp.DeployMode == protocol.DeployReplaceExec {
		logf(protocol.StageReplacing, "替换二进制到 %s", comp.InstallPath)
		// 首次安装时目标目录可能不存在，rename 会直接失败，先创建父目录。
		if err := os.MkdirAll(filepath.Dir(comp.InstallPath), 0755); err != nil {
			logf(protocol.StageFailed, "创建安装目录失败: %v", err)
			return res, err
		}
		if err := binswap.Replace(dlPath, comp.InstallPath); err != nil {
			logf(protocol.StageRollback, "替换失败，执行回滚: %v", err)
			if res.BackupPath != "" {
				binswap.Rollback(res.BackupPath, comp.InstallPath)
				if svcExists() {
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
				if svcExists() {
					svcMgr.Start(comp.ServiceName)
				}
			}
			return res, err
		}
	}
	if comp.DeployMode == protocol.DeployScript {
		logf(protocol.StageExec, "执行安装脚本 %s", dlPath)
		out, err := runScript(ctx, comp, dlPath)
		if strings.TrimSpace(out) != "" {
			logf(protocol.StageExec, "脚本输出:\n%s", strings.TrimSpace(out))
		}
		if err != nil && !comp.IgnoreError {
			logf(protocol.StageRollback, "安装脚本执行失败（脚本模式无平台备份，请查看脚本日志）: %v", err)
			return res, err
		}
	}

	// startService 启动服务；失败时回滚并返回错误。
	startService := func() error {
		logf(protocol.StageStarting, "启动服务 %s", comp.ServiceName)
		if err := svcMgr.Start(comp.ServiceName); err != nil && !comp.IgnoreError {
			logf(protocol.StageRollback, "启服失败，执行回滚: %v", err)
			if res.BackupPath != "" {
				binswap.Rollback(res.BackupPath, comp.InstallPath)
				svcMgr.Start(comp.ServiceName) // 再次尝试启动（用旧版本）
			}
			return err
		}
		time.Sleep(2 * time.Second) // 等待服务启动
		return nil
	}

	// 6. 启服：exec 安装脚本可能刚注册了服务，此处重新探测。
	// 仍不存在时推迟到 post_install 之后再尝试（服务可能由 post_install 注册）。
	pendingStart := false
	if comp.ServiceName != "" {
		if svcExists() {
			if err := startService(); err != nil {
				return res, err
			}
		} else {
			pendingStart = true
			logf(protocol.StageStarting, "服务 %s 尚未注册，推迟到 post_install 之后启动", comp.ServiceName)
		}
	}

	// 7. post_install 钩子：先执行命令数组，再执行 post_install_script。
	if len(comp.PostInstall) > 0 {
		logf(protocol.StagePostInstall, "执行 post_install")
		if err := runHooks(ctx, comp.PostInstall); err != nil && !comp.IgnoreError {
			logf(protocol.StageRollback, "post_install 失败，执行回滚: %v", err)
			if res.BackupPath != "" {
				binswap.Rollback(res.BackupPath, comp.InstallPath)
				if svcExists() {
					svcMgr.Restart(comp.ServiceName)
				}
			}
			return res, err
		}
	}
	if comp.PostInstallScript != "" {
		logf(protocol.StagePostInstall, "执行 post_install 脚本")
		out, err := runPostInstallScript(ctx, comp)
		if strings.TrimSpace(out) != "" {
			logf(protocol.StagePostInstall, "脚本输出:\n%s", strings.TrimSpace(out))
		}
		if err != nil && !comp.IgnoreError {
			logf(protocol.StageRollback, "post_install 脚本失败，执行回滚: %v", err)
			if res.BackupPath != "" {
				binswap.Rollback(res.BackupPath, comp.InstallPath)
				if svcExists() {
					svcMgr.Restart(comp.ServiceName)
				}
			}
			return res, err
		}
	}

	if pendingStart {
		if svcExists() {
			if err := startService(); err != nil {
				return res, err
			}
		} else {
			logf(protocol.StageStarting, "警告：服务 %s 安装后仍未注册，跳过启服（请检查安装脚本或服务名称）", comp.ServiceName)
		}
	}

	// 8. 健康检查（健康检查失败总是回滚，忽略 IgnoreError）
	if len(comp.HealthCmd) > 0 {
		logf(protocol.StageVerifying, "健康检查")
		if !runHealthCheck(ctx, comp.HealthCmd) {
			logf(protocol.StageRollback, "健康检查失败，执行回滚")
			if res.BackupPath != "" {
				binswap.Rollback(res.BackupPath, comp.InstallPath)
				if svcExists() {
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

// runScript 执行 script 模式下载的安装脚本。
//
// 优先用 bash（目标发行版均自带），缺失时回退 /bin/sh；通过解释器执行，
// 避免备份目录挂载 noexec 时直接 exec 失败；
// 脚本参数来自 exec_args，环境变量来自 exec_env，超时来自 exec_timeout。
func runScript(ctx context.Context, comp protocol.ComponentSpec, path string) (string, error) {
	if err := os.Chmod(path, 0755); err != nil {
		return "", fmt.Errorf("设置安装脚本可执行权限失败: %w", err)
	}
	argv := append([]string{shellPath(), path}, comp.ExecArgs...)
	timeout, _ := time.ParseDuration(comp.ExecTimeout)
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	res, err := runner.Run(ctx, runner.Spec{
		Argv: argv, Dir: comp.WorkDir, Env: comp.ExecEnv, Timeout: timeout,
	})
	var stdout string
	if res != nil {
		stdout = res.Stdout
	}
	if err != nil {
		return stdout, fmt.Errorf("安装脚本启动失败: %w", err)
	}
	if res.ExitCode != 0 {
		return stdout, fmt.Errorf("安装脚本退出码 %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return stdout, nil
}

// runPostInstallScript 把组件中的 post_install_script 写入临时文件后执行。
//
// 脚本存放在备份目录（Agent 已可写）而非 /tmp，便于与安装流程使用同一挂载策略；
// 通过解释器执行，避免备份目录挂载 noexec。超时、工作目录与环境变量复用 exec_* 配置。
func runPostInstallScript(ctx context.Context, comp protocol.ComponentSpec) (string, error) {
	dir := comp.BackupDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("创建脚本目录失败: %w", err)
	}
	f, err := os.CreateTemp(dir, "post-install-*.sh")
	if err != nil {
		return "", fmt.Errorf("创建 post_install 脚本失败: %w", err)
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.WriteString(comp.PostInstallScript); err != nil {
		f.Close()
		return "", fmt.Errorf("写入 post_install 脚本失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("写入 post_install 脚本失败: %w", err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		return "", fmt.Errorf("设置 post_install 脚本权限失败: %w", err)
	}

	timeout, _ := time.ParseDuration(comp.ExecTimeout)
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	res, err := runner.Run(ctx, runner.Spec{
		Argv: []string{shellPath(), path}, Dir: comp.WorkDir, Env: comp.ExecEnv, Timeout: timeout,
	})
	var out string
	if res != nil {
		out = strings.TrimSpace(strings.TrimSpace(res.Stdout) + "\n" + strings.TrimSpace(res.Stderr))
	}
	if err != nil {
		return out, fmt.Errorf("post_install 脚本启动失败: %w", err)
	}
	if res.ExitCode != 0 {
		return out, fmt.Errorf("post_install 脚本退出码 %d", res.ExitCode)
	}
	return out, nil
}

// shellPath 选择目标机可用的 Shell：优先 bash，缺失时回退 sh。
func shellPath() string {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
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

// validateScriptURL 在 Agent 侧再次限制 script_url 只能是绝对 http/https URL，
// 防止服务端被攻破后通过 file:// 等协议读取本机文件。
func validateScriptURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if len(raw) > 2048 {
		return fmt.Errorf("地址过长（最多 2048 字符）")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("格式错误: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("仅支持 http/https")
	}
	if u.Host == "" {
		return fmt.Errorf("必须是绝对地址")
	}
	return nil
}

// redactURL 去掉 URL 中的凭据和查询参数，避免 token 被写入任务日志。
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	if u.RawQuery != "" {
		u.RawQuery = "***"
	}
	if u.Fragment != "" {
		u.Fragment = "***"
	}
	return u.String()
}

// artifactFilename 从 URL 的 path 部分取文件名，避免 query 参数进入本地文件名。
func artifactFilename(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		// 平台制品下载地址形如 /api/artifacts/{id}/download。
		// 若直接用 path.Base 会全部得到 "download"，不同组件/版本共用
		// 同一个 .tmp 文件，极易发生旧文件残留导致 Range 416。
		// 这里用制品 ID 生成稳定且唯一的文件名，同时保留可读后缀。
		if id := artifactIDFromPath(u.Path); id != "" {
			return "artifact-" + id + ".bin"
		}
		if base := path.Base(u.Path); base != "." && base != ".." && base != "/" && base != "" {
			return base
		}
	}
	return "artifact"
}

// artifactIDFromPath 从 /api/artifacts/{id}/download 提取制品 ID。
func artifactIDFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "artifacts" && parts[i+2] == "download" {
			id := parts[i+1]
			if id != "" && !strings.ContainsAny(id, `/\`) {
				return id
			}
		}
	}
	return ""
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
