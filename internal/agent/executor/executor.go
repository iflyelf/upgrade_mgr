// Package executor 升级流水线：下载→md5→备份→停服→替换/exec/script→启服→校验→回滚。
package executor

import (
	"context"
	"fmt"
	"log"
	"net/url"
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
func Execute(ctx context.Context, cmd protocol.Command, svcMgr svcmgr.Manager, authHeader string, onStage func(stage, message string)) (*Result, error) {
	res := &Result{ToVersion: cmd.Payload.ToVersion}
	comp := cmd.Payload.Component
	art := cmd.Payload.Artifact
	// 组件表里的默认 backup_dir 是 Linux 路径（/var/lib/...），Windows 主机
	// 需要改用 ProgramData 下的 Agent 备份目录，否则会落到当前盘符根目录。
	comp.BackupDir = resolveBackupDir(comp.BackupDir)

	// 安全：白名单校验 install_path，防止服务端被攻破后下发恶意路径删除任意文件。
	// script 模式与 exec+script_url 模式不读写 install_path（安装位置由脚本自行决定），
	// 因此不做该校验；纯 exec（无制品、无 script_url）允许不填 install_path。
	execScript := comp.DeployMode == protocol.DeployExec && strings.TrimSpace(comp.ScriptURL) != ""
	execOnly := comp.DeployMode == protocol.DeployExec && strings.TrimSpace(art.URL) == "" && !execScript
	if comp.DeployMode != protocol.DeployScript && !execScript && !(execOnly && strings.TrimSpace(comp.InstallPath) == "") {
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

	// 纯 exec 且没有制品：只执行命令，不下载、不备份、不替换任何文件。
	// 组件页「填入示例：只执行命令」即使用该形态，无需先上传任何制品。
	noArtifact := execOnly

	// 1. 下载
	downloadURL := art.URL
	downloadAuth := authHeader
	if (comp.DeployMode == protocol.DeployScript || execScript) && comp.ScriptURL != "" {
		scriptURL := strings.TrimSpace(comp.ScriptURL)
		if strings.HasPrefix(scriptURL, "/") {
			// 平台相对路径（如 /api/external/binary/...）：Agent 在收到指令时已
			// 解析成绝对地址并写入 Artifact.URL，这里直接使用，避免再解析一次。
			// 典型用途：控制台代为缓存外部脚本，老系统（不支持 TLS 1.2）用 http 拉取。
			if strings.TrimSpace(art.URL) == "" {
				logf(protocol.StageFailed, "script_url 为相对路径但缺少已解析的下载地址")
				return res, fmt.Errorf("script_url 相对路径未解析")
			}
			downloadURL = art.URL
			// 平台地址，可以携带 Agent 凭据。
			downloadAuth = authHeader
		} else {
			if err := validateScriptURL(scriptURL); err != nil {
				logf(protocol.StageFailed, "script_url 非法: %v", err)
				return res, fmt.Errorf("script_url 非法: %w", err)
			}
			// 以组件字段为准，避免同一指令里 Artifact.URL 与 ScriptURL 不一致。
			downloadURL = scriptURL
			// 自定义地址可能是第三方仓库，绝不携带平台 Agent 凭据。
			downloadAuth = ""
		}
	}
	dlPath := ""
	if noArtifact {
		logf(protocol.StageDownloading, "exec 模式未指定制品与 script_url，跳过下载，直接执行命令")
	} else {
		logf(protocol.StageDownloading, "下载 %s", redactURL(downloadURL))
		tmpDir := filepath.Join(comp.BackupDir, ".tmp")
		os.MkdirAll(tmpDir, 0755)
		dlPath = filepath.Join(tmpDir, artifactFilename(downloadURL))
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
	}

	// 2. 备份（script / exec+script_url 模式由安装脚本自行处理升级/备份；纯 exec 不替换文件）
	if comp.DeployMode == protocol.DeployScript || execScript {
		logf(protocol.StageBackup, "脚本模式：跳过平台备份，由安装脚本自行处理")
	} else if noArtifact {
		logf(protocol.StageBackup, "exec 模式：仅执行命令，跳过备份与替换")
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
	svcExistedBefore := svcExists() // 记录脚本执行前服务是否存在（区分「被卸载删除」与「本就不存在」）

	// 4. 停服（如果有服务名且服务已存在）
	if comp.ServiceName != "" {
		if !svcExistedBefore {
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
		if execScript {
			// exec + script_url：脚本外置，exec_args 作为脚本参数（如 install/update/uninstall -y），
			// 不再把整段 shell 写进 exec_args。
			logf(protocol.StageExec, "执行远程脚本 %s", redactURL(downloadURL))
			out, err := runScript(ctx, comp, dlPath)
			if strings.TrimSpace(out) != "" {
				logf(protocol.StageExec, "脚本输出:\n%s", strings.TrimSpace(out))
			}
			if err != nil && !comp.IgnoreError {
				logf(protocol.StageRollback, "脚本执行失败（exec 模式无平台备份，请查看脚本日志）: %v", err)
				return res, err
			}
		} else {
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
	//
	// 卸载动作（exec_args 首个动作为 uninstall/remove/purge）一律不启服：
	// 卸载示例填写 service_name 只用于对齐与停服，脚本删除服务后再启服必然失败
	// （Windows「已标记删除」、systemd 缓存未刷新等边缘情况尤甚）。
	pendingStart := false
	if comp.ServiceName != "" && protocol.IsUninstallAction(comp.ExecArgs) {
		switch {
		case serviceGone(svcMgr, comp.ServiceName) && svcExistedBefore:
			logf(protocol.StageStarting, "服务 %s 已被卸载脚本删除，跳过启动", comp.ServiceName)
		case !svcExistedBefore:
			logf(protocol.StageStarting, "服务 %s 卸载前就不存在（可能已卸载过），跳过启动", comp.ServiceName)
		default:
			logf(protocol.StageStarting, "警告：卸载后服务 %s 仍存在，跳过启动（请检查卸载脚本输出）", comp.ServiceName)
		}
	} else if comp.ServiceName != "" {
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
		// 服务声明了但安装脚本没注册：先给出根因，避免只看到“健康检查失败”。
		if pendingStart && comp.ServiceName != "" && !svcExists() {
			logf(protocol.StageVerifying,
				"服务 %s 未注册，健康检查大概率失败；请检查安装脚本输出（是否以管理员运行、下载是否成功）",
				comp.ServiceName)
		}
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
// Linux 优先 bash，缺失时回退 /bin/sh；Windows 按脚本扩展名选择 PowerShell 或 cmd。
// 通过解释器执行，避免备份目录挂载 noexec 时直接 exec 失败；
// 脚本参数来自 exec_args，环境变量来自 exec_env，超时来自 exec_timeout。
func runScript(ctx context.Context, comp protocol.ComponentSpec, scriptPath string) (string, error) {
	// 下载内容校验：空文件或 HTML 错误页（代理/网关返回 200）会让解释器
	// 立刻以 0 退出且没有任何输出，任务随后卡在「服务未注册 / 健康检查失败」，
	// 掩盖真实原因。这里提前拒绝并给出明确错误。
	if err := validateScriptContent(scriptPath); err != nil {
		return "", err
	}
	// Windows 上下载的制品名可能没有脚本扩展名（如 artifact-1.bin），
	// 需要先落到带 .ps1 的路径，PowerShell -File 才能执行。
	prepared, cleanup, err := prepareScriptPath(scriptPath)
	if err != nil {
		return "", err
	}
	defer cleanup()
	scriptPath = prepared
	if err := os.Chmod(scriptPath, 0755); err != nil {
		// Windows 上 Chmod 仅切换只读属性，通常不会失败；失败不阻断执行。
		log.Printf("⚠️ 设置安装脚本可执行权限失败（继续执行）: %v", err)
	}
	argv, err := scriptArgv(comp, scriptPath)
	if err != nil {
		return "", err
	}
	timeout, _ := time.ParseDuration(comp.ExecTimeout)
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	res, err := runner.Run(ctx, runner.Spec{
		Argv: argv, Dir: comp.WorkDir, Env: comp.ExecEnv, Timeout: timeout,
	})
	var out string
	if res != nil {
		out = combineOutput(res.Stdout, res.Stderr)
	}
	if err != nil {
		return out, fmt.Errorf("安装脚本启动失败: %w", err)
	}
	if res.ExitCode != 0 {
		return out, fmt.Errorf("安装脚本退出码 %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return out, nil
}

// combineOutput 合并 stdout 与 stderr，供任务日志展示。
//
// 只返回 stdout 会丢掉关键信息：PowerShell 的部分错误（Write-Error/异常信息）
// 走 stderr，脚本退出码仍为 0 时日志里就只剩半截输出，无法定位问题。
func combineOutput(stdout, stderr string) string {
	out := strings.TrimRight(strings.TrimSpace(stdout), "\n")
	if errText := strings.TrimSpace(stderr); errText != "" {
		if out != "" {
			out += "\n"
		}
		out += errText
	}
	return out
}

// validateScriptContent 校验下载到的脚本内容是否可执行。
//
// 覆盖最常见的两类“假成功”：
//   - 空文件（下载中断但返回 200）；
//   - HTML 错误页（代理/网关拦截，返回 200 + 页面内容）。
//
// 其余情况（如 .exe、有效的 shell/PowerShell 脚本）原样放行。
func validateScriptContent(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("读取安装脚本失败: %w", err)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("安装脚本为空（下载可能被中断或代理返回了空响应）: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("读取安装脚本失败: %w", err)
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	head := strings.TrimSpace(strings.ToLower(string(buf[:n])))
	for _, prefix := range []string{"<!doctype", "<html", "<head", "<body", "<?xml"} {
		if strings.HasPrefix(head, prefix) {
			return fmt.Errorf("安装脚本内容不是脚本，而是 HTML/XML 页面（代理或网关拦截）：%s", path)
		}
	}
	return nil
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
	f, err := os.CreateTemp(dir, "post-install-*"+scriptExt(""))
	if err != nil {
		return "", fmt.Errorf("创建 post_install 脚本失败: %w", err)
	}
	scriptPath := f.Name()
	defer os.Remove(scriptPath)
	if _, err := f.WriteString(comp.PostInstallScript); err != nil {
		f.Close()
		return "", fmt.Errorf("写入 post_install 脚本失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("写入 post_install 脚本失败: %w", err)
	}
	_ = os.Chmod(scriptPath, 0700)

	// 与安装脚本相同的平台预处理：Windows 需要 UTF-8 BOM + CRLF，
	// 否则 PowerShell 2.0 会把含中文的 post_install 脚本按 ANSI 解析。
	prepared, cleanup, prepErr := prepareScriptPath(scriptPath)
	if prepErr != nil {
		return "", prepErr
	}
	defer cleanup()
	scriptPath = prepared

	timeout, _ := time.ParseDuration(comp.ExecTimeout)
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	argv, err := scriptArgv(comp, scriptPath)
	if err != nil {
		return "", err
	}
	res, err := runner.Run(ctx, runner.Spec{
		Argv: argv, Dir: comp.WorkDir, Env: comp.ExecEnv, Timeout: timeout,
	})
	var out string
	if res != nil {
		out = combineOutput(res.Stdout, res.Stderr)
	}
	if err != nil {
		return out, fmt.Errorf("post_install 脚本启动失败: %w", err)
	}
	if res.ExitCode != 0 {
		return out, fmt.Errorf("post_install 脚本退出码 %d", res.ExitCode)
	}
	return out, nil
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
		// 先把 URL 解码后的反斜杠统一成 /，再取基名：Windows 上
		//   %5c 解码出的 "..\\evil" 否则会被 filepath.Join 当作上级目录。
		base := filepath.Base(strings.ReplaceAll(u.Path, "\\", "/"))
		if base != "." && base != ".." && base != "/" && base != "" && !strings.ContainsAny(base, `\\/`) {
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

// serviceGone 判断服务是否已删除或标记删除（Windows DELETE_PENDING）。
// 对于不支持「标记删除」检测的管理器，等价于 !Exists()。
func serviceGone(mgr svcmgr.Manager, name string) bool {
	if !mgr.Exists(name) {
		return true
	}
	// Windows：服务可能处于「已标记删除」；systemd：unit 文件已删但缓存未刷新。
	// 由管理器的 Removed 探测；未实现的管理器以 Exists 为准。
	type removalChecker interface {
		Removed(name string) bool
	}
	if rc, ok := mgr.(removalChecker); ok {
		return rc.Removed(name)
	}
	return false
}
