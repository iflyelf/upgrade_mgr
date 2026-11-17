// install-windows-monitor.exe — Windows 一体化监控安装器。
//
// 与 Linux 的 install-monitor.sh 对齐，支持 install / update / uninstall / status：
//   - 安装/更新 upgrade-agent（平台 windows-<arch> 二进制，--force 重新注册）
//   - 安装/更新 windows_exporter（复用 examples/windows_exporter/install.ps1，
//     默认 C:\monitor\exporter，含自动重启/注册表清理）
//   - 安装前清理旧的 Zabbix / windows_exporter 服务、目录、计划任务
//
// 构建（任意平台交叉编译）：
//
//	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" \
//	  -o dist/install-windows-monitor.exe ./cmd/install-monitor
//
// 仅使用控制台内网地址，不访问公网。接入令牌由控制台
// /api/agent/install-monitor/token 提供（永久自动化令牌）。
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultServer    = "http://console.example.com"
	agentService     = "upgrade-agent"
	exporterService  = "windows_exporter"
	agentInstallDir  = `C:\Program Files\upgrade-agent`
	agentExePath     = `C:\Program Files\upgrade-agent\upgrade-agent.exe`
	exporterInstall  = `C:\monitor\exporter`
	exporterExePath  = `C:\monitor\exporter\windows_exporter.exe`
	maxDownloadBytes = 200 << 20
)

var (
	logFile *os.File
	opts    options
)

type options struct {
	Action string
	Yes    bool
	Server string
	Token  string
	Arch   string
}

func main() {
	initLogger()
	if logFile != nil {
		defer logFile.Close()
	}
	o, err := parseArgs(os.Args[1:])
	if err != nil {
		usage()
		fatal("%v", err)
	}
	if o.Action == "help" {
		usage()
		return
	}
	opts = *o
	if opts.Action == "" {
		opts.Action = "install"
	}
	opts.Server = resolveServer(opts.Server)
	mustRunAsAdmin()

	log.Printf("install-monitor: action=%s server=%s", opts.Action, opts.Server)
	switch opts.Action {
	case "install":
		legacyCleanup(true)
		token := resolveToken()
		installAgent(token)
		installExporter("install")
		log.Printf("安装完成")
	case "update":
		// 更新时保留现有 windows_exporter 服务/注册表，只有安装才清理旧服务。
		legacyCleanup(false)
		token := resolveToken()
		installAgent(token)
		installExporter("update")
		log.Printf("更新完成")
	case "uninstall":
		if !opts.Yes {
			if !confirm("确定卸载 upgrade-agent 与 windows_exporter，并清理旧监控吗？输入 yes 继续: ") {
				log.Printf("已取消")
				return
			}
		}
		uninstallExporter()
		uninstallAgent()
		legacyCleanup(true)
		log.Printf("卸载完成")
	case "status":
		printStatus()
	default:
		usage()
		fatal("未知动作: %s", opts.Action)
	}
}

func usage() {
	fmt.Println(`用法: install-windows-monitor.exe [install|update|uninstall|status] [-y] [--server URL] [--token TOKEN] [--arch amd64|arm64]

  install     安装/覆盖安装 Agent + Windows Exporter（默认）
  update      更新 Agent + Windows Exporter
  uninstall   卸载 Agent + Windows Exporter，并清理旧监控
  status      查看服务与文件状态
  -y, --yes   卸载时跳过确认
  --server    控制台地址（默认 http://console.example.com）
  --token     直接指定接入令牌（默认从控制台获取永久令牌）
  --arch      强制指定架构（默认按 PROCESSOR_ARCHITECTURE 自动识别）`)
}

func parseArgs(args []string) (*options, error) {
	o := &options{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-y" || a == "--yes":
			o.Yes = true
		case a == "-h" || a == "--help":
			o.Action = "help"
		case a == "--server":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--server 缺少参数")
			}
			i++
			o.Server = args[i]
		case strings.HasPrefix(a, "--server="):
			o.Server = strings.TrimPrefix(a, "--server=")
		case a == "--token":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--token 缺少参数")
			}
			i++
			o.Token = args[i]
		case strings.HasPrefix(a, "--token="):
			o.Token = strings.TrimPrefix(a, "--token=")
		case a == "--arch":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--arch 缺少参数")
			}
			i++
			o.Arch = args[i]
		case strings.HasPrefix(a, "--arch="):
			o.Arch = strings.TrimPrefix(a, "--arch=")
		case !strings.HasPrefix(a, "-"):
			if o.Action != "" {
				return nil, fmt.Errorf("多余的动作参数: %s", a)
			}
			o.Action = strings.ToLower(a)
		default:
			return nil, fmt.Errorf("未知参数: %s", a)
		}
	}
	return o, nil
}

func initLogger() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	dir := os.TempDir()
	if v := os.Getenv("SystemRoot"); v != "" {
		dir = filepath.Join(v, "Temp")
	}
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "install-monitor.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.SetOutput(os.Stdout)
		return
	}
	logFile = f
	log.SetOutput(io.MultiWriter(os.Stdout, f))
	log.Printf("日志文件: %s", path)
}

func mustRunAsAdmin() {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	test := filepath.Join(root, "Temp", fmt.Sprintf("install-monitor-admin-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(test, 0o755); err != nil {
		fatal("需要以管理员身份运行: %v", err)
	}
	_ = os.RemoveAll(test)
}

func resolveServer(v string) string {
	if v == "" {
		v = os.Getenv("UPGRADE_MGR_EXTERNAL_URL")
	}
	if v == "" {
		v = os.Getenv("UPGRADE_AGENT_SERVER_URL")
	}
	if v == "" {
		v = defaultServer
	}
	return strings.TrimRight(strings.TrimSpace(v), "/")
}

func resolveArch(v string) string {
	if v != "" {
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case "x86_64", "amd64":
			return "amd64"
		case "aarch64", "arm64":
			return "arm64"
		}
		fatal("不支持的架构: %s", v)
	}
	envArch := os.Getenv("PROCESSOR_ARCHITEW6432")
	if envArch == "" {
		envArch = os.Getenv("PROCESSOR_ARCHITECTURE")
	}
	switch strings.ToUpper(strings.TrimSpace(envArch)) {
	case "ARM64":
		return "arm64"
	case "AMD64":
		return "amd64"
	}
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	default:
		return "amd64"
	}
}

func resolveToken() string {
	if opts.Token != "" {
		return strings.TrimSpace(opts.Token)
	}
	body, err := downloadString(opts.Server + "/api/agent/install-monitor/token")
	if err != nil {
		fatal("未获取到接入令牌，请先在控制台「接入主机 → 生成接入脚本」生成: %v", err)
	}
	token := strings.TrimSpace(body)
	if token == "" {
		fatal("控制台返回的接入令牌为空，请重新生成接入脚本")
	}
	if !strings.HasPrefix(token, "enr_auto_") {
		fatal("控制台返回的接入令牌格式不正确，请重新生成接入脚本")
	}
	return token
}

func installAgent(token string) {
	arch := resolveArch(opts.Arch)
	tmp := filepath.Join(os.TempDir(), "upgrade-agent.exe")
	defer os.Remove(tmp)
	url := opts.Server + "/api/agent/binary/windows-" + arch
	log.Printf("下载 Agent: %s", url)
	must(downloadTo(url, tmp))
	log.Printf("安装/重新注册 Agent（--force）...")
	must(run(tmp, "install", "--server", opts.Server, "--enroll-token", token, "--force"))
}

func installExporter(action string) {
	// 更新时如果服务或新目录不存在（例如从旧目录迁移），退化为 install：
	// install.ps1 的 install 动作本身支持覆盖升级与失败回滚。
	if action == "update" && (!fileExists(exporterExePath) || serviceMissing(exporterService)) {
		log.Printf("Windows Exporter 尚未安装或不在 %s，改用 install 动作", exporterInstall)
		action = "install"
	}
	ps1 := filepath.Join(os.TempDir(), "windows-exporter-install.ps1")
	defer os.Remove(ps1)
	url := opts.Server + "/api/external/binary/windows-exporter-install.ps1"
	log.Printf("下载 Windows Exporter 安装脚本: %s", url)
	must(downloadTo(url, ps1))

	// 直接指定平台二进制地址，避免目标机尝试 GitHub/代理；同时按 NT 版本
	// 选择 latest / legacy 资产（Server 2008 R2 ~ 2012 R2 必须用 legacy）。
	arch := resolveArch(opts.Arch)
	asset := "windows-exporter-" + arch
	if windowsMajorVersion() < 10 {
		asset += "-legacy"
	}
	binURL := opts.Server + "/api/external/binary/" + asset
	log.Printf("执行 Windows Exporter %s（%s）...", action, binURL)
	must(run(powershellPath(), "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", ps1, action, "-Yes", "-InstallDir", exporterInstall, "-BinaryUrl", binURL))
}

func uninstallExporter() {
	ps1 := filepath.Join(os.TempDir(), "windows-exporter-install.ps1")
	if err := downloadTo(opts.Server+"/api/external/binary/windows-exporter-install.ps1", ps1); err == nil {
		if err := run(powershellPath(), "-NoProfile", "-ExecutionPolicy", "Bypass",
			"-File", ps1, "uninstall", "-Yes", "-InstallDir", exporterInstall); err != nil {
			log.Printf("Windows Exporter 卸载脚本失败，执行兜底清理: %v", err)
			fallbackRemoveExporter()
		}
		_ = os.Remove(ps1)
		return
	}
	fallbackRemoveExporter()
}

func fallbackRemoveExporter() {
	log.Printf("兜底清理 Windows Exporter 服务与目录")
	runQuiet("sc", "stop", exporterService)
	runQuiet("sc", "delete", exporterService)
	runQuiet("reg", "delete", `HKLM\SYSTEM\CurrentControlSet\Services\`+exporterService, "/f")
	_ = os.RemoveAll(exporterInstall)
}

func uninstallAgent() {
	if fileExists(agentExePath) {
		log.Printf("卸载 Agent 服务...")
		if err := run(agentExePath, "uninstall"); err != nil {
			log.Printf("Agent uninstall 返回错误: %v", err)
		}
	}
	// Agent 二进制缺失时的兜底清理。
	runQuiet("sc", "stop", agentService)
	runQuiet("sc", "delete", agentService)
	runQuiet("schtasks", "/delete", "/tn", "upgrade-agent-watchdog", "/f")
	_ = os.RemoveAll(agentInstallDir)
	if pd := os.Getenv("ProgramData"); pd != "" {
		_ = os.RemoveAll(filepath.Join(pd, "upgrade-agent"))
	} else {
		_ = os.RemoveAll(`C:\ProgramData\upgrade-agent`)
	}
}

func printStatus() {
	for _, svc := range []string{agentService, exporterService} {
		out, err := exec.Command("sc", "query", svc).CombinedOutput()
		if err != nil {
			log.Printf("服务 %s: 未安装或查询失败 (%v)", svc, err)
			continue
		}
		log.Printf("服务 %s:\n%s", svc, strings.TrimSpace(string(out)))
	}
	log.Printf("Agent 路径: %s（存在: %v）", agentExePath, fileExists(agentExePath))
	log.Printf("Windows Exporter 路径: %s（存在: %v）", exporterExePath, fileExists(exporterExePath))
	if fileExists(agentExePath) {
		if out, err := exec.Command(agentExePath, "version").CombinedOutput(); err == nil {
			log.Printf("Agent 版本: %s", strings.TrimSpace(string(out)))
		}
	}
	if fileExists(exporterExePath) {
		if out, err := exec.Command(exporterExePath, "--version").CombinedOutput(); err == nil {
			log.Printf("Windows Exporter 版本: %s", strings.TrimSpace(string(out)))
		}
	}
}

// legacyCleanup 清理旧 Zabbix / windows_exporter 环境。
//
// cleanExporter=true 时同时删除旧的 windows_exporter 服务/注册表（安装/卸载）；
// 更新时必须传 false，否则会把正在更新的 Windows Exporter 服务删掉。
//
// LEGACY-CLEANUP: 旧监控清理段。确认所有主机已迁移到新监控后，
// 可删除本函数及 main 中的调用，不影响新 Agent/Windows Exporter 安装。
func legacyCleanup(cleanExporter bool) {
	log.Printf("[legacy] 清理旧监控服务、目录与计划任务...")
	services := []string{"Zabbix Agent", "Zabbix Agent 2"}
	if cleanExporter {
		services = append(services, exporterService)
	}
	for _, svc := range services {
		runQuiet("sc", "stop", svc)
		runQuiet("sc", "delete", svc)
	}
	// 旧服务/事件日志注册表项。
	keys := []string{
		`HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services\EventLog\System\Zabbix Agent`,
		`HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services\EventLog\Application\Zabbix Agent 2`,
	}
	if cleanExporter {
		keys = append(keys, `HKLM\SYSTEM\CurrentControlSet\Services\windows_exporter`)
	}
	for _, key := range keys {
		runQuiet("reg", "delete", key, "/f")
	}
	// 旧 Zabbix Agent 1 / 2 自带 uninstall。
	if fileExists(`C:\zabbix\sbin\zabbix_agentd.exe`) {
		runQuiet(`C:\zabbix\sbin\zabbix_agentd.exe`, "--config", `C:\zabbix\etc\zabbix_agentd.conf`, "--stop")
		runQuiet(`C:\zabbix\sbin\zabbix_agentd.exe`, "--config", `C:\zabbix\etc\zabbix_agentd.conf`, "--uninstall")
	}
	if fileExists(`C:\zabbix_agentd\bin\zabbix_agent2.exe`) {
		runQuiet(`C:\zabbix_agentd\bin\zabbix_agent2.exe`, "-c", `C:\zabbix_agentd\zabbix_agent2.conf`, "-stop")
		runQuiet(`C:\zabbix_agentd\bin\zabbix_agent2.exe`, "-c", `C:\zabbix_agentd\zabbix_agent2.conf`, "-uninstall")
	}
	// 旧安装目录与升级脚本目录。
	_ = os.RemoveAll(`C:\zabbix_agentd`)
	_ = os.RemoveAll(`C:\zabbix`)
	_ = os.RemoveAll(`C:\scripts`)
	// 旧 Zabbix 升级计划任务。
	runQuiet("schtasks", "/delete", "/tn", "Upgrade Zabbix Agent", "/f")
	// 旧临时文件。
	cleanupLegacyTemp()
	log.Printf("[legacy] 旧监控清理完成")
}

func cleanupLegacyTemp() {
	root := os.Getenv("SystemRoot")
	if root == "" {
		return
	}
	dir := filepath.Join(root, "Temp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if strings.HasPrefix(name, "zabbix") {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

func downloadTo(url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		if err := downloadOnce(url, dest); err == nil {
			return nil
		} else {
			lastErr = err
			log.Printf("下载失败（%d/4）: %v", attempt, err)
			time.Sleep(time.Duration(attempt*3) * time.Second)
		}
	}
	return lastErr
}

func downloadOnce(url, dest string) error {
	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}
	tmp := dest + ".downloading"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxDownloadBytes+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n > maxDownloadBytes || n == 0 {
		os.Remove(tmp)
		return fmt.Errorf("写入失败或文件为空: %v %v size=%d", copyErr, closeErr, n)
	}
	_ = os.Remove(dest)
	if err := os.Rename(tmp, dest); err != nil {
		if copyErr := copyFile(tmp, dest); copyErr != nil {
			os.Remove(tmp)
			return copyErr
		}
		os.Remove(tmp)
	}
	return nil
}

func downloadString(url string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func powershellPath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if fileExists(p) {
			return p
		}
	}
	return "powershell.exe"
}

func run(name string, args ...string) error {
	log.Printf("执行: %s %s", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		log.Printf("%s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func runQuiet(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	_ = cmd.Run()
}

func serviceMissing(name string) bool {
	out, err := exec.Command("sc", "query", name).CombinedOutput()
	if err != nil {
		return true
	}
	return !strings.Contains(strings.ToUpper(string(out)), "RUNNING") &&
		!strings.Contains(strings.ToUpper(string(out)), "STOPPED")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	var answer string
	_, _ = fmt.Scanln(&answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func must(err error) {
	if err != nil {
		fatal("%v", err)
	}
}

func fatal(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("FATAL: %s", msg)
	fmt.Fprintln(os.Stderr, msg)
	if logFile != nil {
		fmt.Fprintf(os.Stderr, "详细日志: %s\n", logFile.Name())
	}
	os.Exit(1)
}
