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
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
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
	exporterPort     = 16666
)

var (
	logFile         *os.File
	opts            options
	interactiveMode bool
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
	interactiveMode = len(os.Args) <= 1
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
	interactive := opts.Action == ""
	if interactive {
		action, ok := menu()
		if !ok {
			return
		}
		opts.Action = action
	}
	opts.Server = resolveServer(opts.Server)
	if opts.Action != "status" {
		mustRunAsAdmin()
	}

	log.Printf("install-monitor: action=%s server=%s", opts.Action, opts.Server)
	switch opts.Action {
	case "install":
		// 先安装/更新 Agent，确保旧监控清理期间仍能保持平台连接（防止失联）。
		token := resolveToken()
		installAgent(token)
		legacyCleanup(true)
		checkExporterPort()
		installExporter("install")
		verifyExporter("安装")
	case "update":
		// 先更新 Agent；更新时保留现有 windows_exporter 服务/注册表。
		token := resolveToken()
		installAgent(token)
		legacyCleanup(false)
		checkExporterPort()
		installExporter("update")
		verifyExporter("更新")
	case "uninstall":
		if !opts.Yes {
			if !confirm("确定卸载 upgrade-agent 与 windows_exporter，并清理旧监控吗？输入 yes 继续: ") {
				log.Printf("已取消")
				break
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
	if interactive {
		fmt.Print("\n按回车键退出...")
		var dummy string
		_, _ = fmt.Scanln(&dummy)
	}
}

// menu 在无参数双击运行时提供数字菜单，兼容旧安装器的操作习惯。
func menu() (string, bool) {
	for {
		fmt.Println()
		fmt.Println("========================================")
		fmt.Println("         监控一体化安装器")
		fmt.Println("========================================")
		fmt.Println("  1. 安装（Agent + Windows Exporter）")
		fmt.Println("  2. 更新")
		fmt.Println("  3. 卸载")
		fmt.Println("  4. 查看状态")
		fmt.Println("  0. 退出")
		fmt.Print("请输入数字: ")
		var choice string
		if _, err := fmt.Scanln(&choice); err != nil {
			return "", false
		}
		switch strings.TrimSpace(choice) {
		case "1":
			return "install", true
		case "2":
			return "update", true
		case "3":
			return "uninstall", true
		case "4":
			return "status", true
		case "0":
			return "", false
		default:
			fmt.Println("无效输入，请重新输入")
		}
	}
}

func usage() {
	fmt.Println(`用法: install-windows-monitor.exe [install|update|uninstall|status] [-y] [--server URL] [--token TOKEN] [--arch amd64|arm64]

  直接双击/无参数运行会显示数字菜单（1 安装 / 2 更新 / 3 卸载 / 4 状态）。
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
		fatal("未获取到接入令牌（控制台会自动创建，请检查控制台是否可访问、数据库是否正常）: %v", err)
	}
	token := strings.TrimSpace(body)
	if token == "" {
		fatal("控制台返回的接入令牌为空，请检查控制台日志和数据库连接")
	}
	if !strings.HasPrefix(token, "enr_auto_") {
		fatal("控制台未返回有效接入令牌（令牌会自动创建，无需人工生成）；返回内容: %.200s", token)
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
	must(runTimeout(10*time.Minute, tmp, "install", "--server", opts.Server, "--enroll-token", token, "--force"))
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
	must(downloadToTimeout(url, ps1, 60*time.Second, 3))

	// 与「任务中的脚本」保持一致：注入平台地址，让 install.ps1 自己按 OS 选择
	// legacy/latest 并优先走平台离线缓存。之前强制 -BinaryUrl 指向 legacy 资产，
	// 在部分 Server 2012 R2 上脚本会提前退出（且输出被吞，只剩 exit status 1）。
	env := mergeEnv(os.Environ(),
		"UPGRADE_MGR_EXTERNAL_URL="+opts.Server,
		"UPGRADE_AGENT_SERVER_URL="+opts.Server,
	)
	baseArgs := []string{"-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", ps1, action, "-Yes", "-InstallDir", exporterInstall}
	log.Printf("执行 Windows Exporter %s（平台地址 %s）...", action, opts.Server)
	if err := runPowerShell(env, baseArgs...); err != nil {
		if strings.Contains(err.Error(), "超时") {
			fatal("Windows Exporter %s 超时，请查看 %s 中的最后输出", action,
				filepath.Join(os.TempDir(), "windows-exporter-install.log"))
		}
		// 回退：显式指定平台二进制地址（旧缓存/脚本异常时仍可安装）。
		arch := resolveArch(opts.Arch)
		asset := "windows-exporter-" + arch
		if windowsMajorVersion() < 10 {
			asset += "-legacy"
		}
		binURL := opts.Server + "/api/external/binary/" + asset
		log.Printf("WARN: 默认安装失败（%v），回退指定 -BinaryUrl 重试: %s", err, binURL)
		must(runPowerShell(env, append(baseArgs, "-BinaryUrl", binURL)...))
	}
}

// portInUse 检测本机端口是否已有程序监听（1 秒超时，避免探测卡死）。
func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// checkExporterPort 安装前提示 16666 端口占用；不阻断安装，完成后由探测给出结论。
func checkExporterPort() {
	if !portInUse(exporterPort) {
		return
	}
	if !serviceMissing(exporterService) {
		log.Printf("端口 %d 已由现有 %s 服务监听（更新场景正常）", exporterPort, exporterService)
		return
	}
	log.Printf("WARN: 端口 %d 已被其它程序占用，Windows Exporter 可能无法启动", exporterPort)
	if out, err := exec.Command("cmd", "/c",
		fmt.Sprintf("netstat -ano | findstr :%d", exporterPort)).CombinedOutput(); err == nil {
		log.Printf("端口占用:\n%s", strings.TrimSpace(string(out)))
	}
}

// probeExporterMetrics 带超时和重试地探测 /metrics，避免服务异常时安装器卡死。
func probeExporterMetrics() error {
	client := &http.Client{Timeout: 3 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/metrics", exporterPort)
	var lastErr error
	for i := 1; i <= 10; i++ {
		resp, err := client.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK &&
				bytes.Contains(body, []byte("windows_")) {
				log.Printf("指标探测成功（第 %d 次）：%s", i, url)
				return nil
			}
			if readErr != nil {
				lastErr = readErr
			} else {
				lastErr = fmt.Errorf("HTTP %d 或返回内容不含 windows_ 指标", resp.StatusCode)
			}
		} else {
			lastErr = err
		}
		if i < 10 {
			time.Sleep(time.Second)
		}
	}
	return fmt.Errorf("%s 重试 10 次仍不可用: %v", url, lastErr)
}

// verifyExporter 安装/更新结束后给出明确的成功或失败结论，失败时打印诊断信息。
func verifyExporter(action string) {
	if err := probeExporterMetrics(); err == nil {
		log.Printf("✅ %s成功：Agent 已接入，监控指标正常", action)
		return
	} else {
		log.Printf("WARN: 监控指标探测失败: %v", err)
		if portInUse(exporterPort) {
			log.Printf("端口 %d 正在监听，但 /metrics 不可用或内容异常，可能是其它程序占用", exporterPort)
		} else {
			log.Printf("端口 %d 未监听，Windows Exporter 服务可能未启动", exporterPort)
		}
	}
	if out, err := exec.Command("sc", "query", exporterService).CombinedOutput(); err == nil {
		log.Printf("服务状态:\n%s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("cmd", "/c",
		fmt.Sprintf("netstat -ano | findstr :%d", exporterPort)).CombinedOutput(); err == nil {
		log.Printf("端口占用:\n%s", strings.TrimSpace(string(out)))
	}
	fatal("%s失败：Windows Exporter 指标不可用（Agent 已安装，可远程排查后重试）", action)
}

// uninstallExporter 直接本地清理，不下载/执行 PowerShell 卸载脚本。
//
// install.ps1 在某些 Windows 版本上会卡在服务停止/目录删除阶段（实际已卸载但
// PowerShell 进程不退出）。卸载是确定性操作，直接用 sc/reg/netsh/文件删除即可，
// 也避免平台不可达时等待脚本下载。
func uninstallExporter() {
	log.Printf("卸载 Windows Exporter（本地清理）...")
	serviceDir := exporterServiceDir()
	if serviceDir != "" {
		log.Printf("服务实际安装目录: %s", serviceDir)
	}
	runQuietTimeout(30*time.Second, "sc", "stop", exporterService)
	runQuietTimeout(30*time.Second, "sc", "delete", exporterService)
	runQuietTimeout(30*time.Second, "reg", "delete",
		`HKLM\SYSTEM\CurrentControlSet\Services\`+exporterService, "/f")
	runQuietTimeout(30*time.Second, "netsh", "advfirewall", "firewall", "delete",
		"rule", "name=windows_exporter (TCP 16666)")
	// 服务删除后仍可能有手工启动的 exporter 进程占用 exe，先结束再删目录。
	runQuietTimeout(20*time.Second, "taskkill", "/F", "/IM", "windows_exporter.exe")
	_ = os.RemoveAll(exporterInstall)
	if serviceDir != "" && !strings.EqualFold(serviceDir, exporterInstall) &&
		fileExists(filepath.Join(serviceDir, "windows_exporter.exe")) {
		log.Printf("清理历史安装目录: %s", serviceDir)
		_ = os.RemoveAll(serviceDir)
	}
	_ = os.RemoveAll(`C:\monitor\windows_exporter\textfile_inputs`)
	log.Printf("Windows Exporter 本地清理完成")
}

// exporterServiceDir 从 sc qc 输出解析服务实际安装目录（删除服务前调用）。
func exporterServiceDir() string {
	out, err := exec.Command("sc", "qc", exporterService).CombinedOutput()
	if err != nil {
		return ""
	}
	return parseBinPathDir(string(out))
}

// parseBinPathDir 从 sc qc 的 BINARY_PATH_NAME 行提取目录，兼容带引号/不带引号。
func parseBinPathDir(out string) string {
	for _, line := range strings.Split(out, "\n") {
		upper := strings.ToUpper(line)
		idx := strings.Index(upper, "BINARY_PATH_NAME")
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+len("BINARY_PATH_NAME"):])
		rest = strings.TrimLeft(rest, ":")
		rest = strings.TrimSpace(rest)
		if rest == "" {
			continue
		}
		if rest[0] == '"' {
			if end := strings.Index(rest[1:], `"`); end >= 0 {
				return windowsDir(rest[1 : end+1])
			}
		}
		// 不带引号时路径可能含空格：取第一个 .exe 之前的完整路径。
		if i := strings.Index(strings.ToLower(rest), ".exe"); i >= 0 {
			return windowsDir(rest[:i+4])
		}
	}
	return ""
}

func windowsDir(p string) string {
	p = strings.TrimSpace(strings.Trim(p, `"`))
	if i := strings.LastIndexAny(p, `\/`); i > 0 {
		return p[:i]
	}
	return ""
}

func uninstallAgent() {
	if fileExists(agentExePath) {
		log.Printf("卸载 Agent 服务...")
		if err := runTimeout(2*time.Minute, agentExePath, "uninstall"); err != nil {
			log.Printf("Agent uninstall 返回错误或超时: %v", err)
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
	if !serviceMissing(exporterService) {
		checkExporterPort()
		if err := probeExporterMetrics(); err == nil {
			log.Printf("指标探测: 正常（http://127.0.0.1:%d/metrics）", exporterPort)
		} else {
			log.Printf("指标探测: 失败（%v）", err)
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
	return downloadToTimeout(url, dest, 15*time.Minute, 4)
}

// downloadToTimeout 带单次超时与重试次数的下载；脚本类小文件用短超时避免卸载卡住。
func downloadToTimeout(url, dest string, timeout time.Duration, attempts int) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := downloadOnceTimeout(url, dest, timeout); err == nil {
			return nil
		} else {
			lastErr = err
			log.Printf("下载失败（%d/%d）: %v", attempt, attempts, err)
			if attempt < attempts {
				time.Sleep(time.Duration(attempt*2) * time.Second)
			}
		}
	}
	return lastErr
}

func downloadOnceTimeout(url, dest string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
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

// mergeEnv 合并环境变量：同名（Windows 大小写不敏感）以后传入的值为准，
// 避免系统里已有的旧 UPGRADE_MGR_EXTERNAL_URL 覆盖本次 --server。
func mergeEnv(base []string, pairs ...string) []string {
	drop := make(map[string]bool, len(pairs))
	for _, kv := range pairs {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			drop[strings.ToUpper(kv[:i])] = true
		}
	}
	out := make([]string, 0, len(base)+len(pairs))
	for _, kv := range base {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		if drop[strings.ToUpper(k)] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, pairs...)
}

// runPowerShell 执行 PowerShell，并把 stdout/stderr 同时写入临时日志文件。
//
// 某些 PowerShell 版本在 stdout 被重定向为管道时 [Console]::Out 可能丢失输出，
// 写文件再读回可以确保安装失败时能看到 install.ps1 的真实错误，而不是只有 exit status 1。
func runPowerShell(env []string, args ...string) error {
	ps := powershellPath()
	log.Printf("执行: %s %s", ps, strings.Join(args, " "))
	logPath := filepath.Join(os.TempDir(), "windows-exporter-install.log")
	log.Printf("PowerShell 最长运行 15 分钟，实时输出写入 %s（结束后打印末尾）", logPath)
	f, err := os.Create(logPath)
	if err != nil {
		return runWithEnv(env, ps, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, args...)
	cmd.Env = env
	cmd.Stdout = f
	cmd.Stderr = f
	runErr := cmd.Run()
	_ = f.Close()
	data, _ := os.ReadFile(logPath)
	if len(data) > 0 {
		log.Printf("PowerShell 输出（%s）:\n%s", logPath, tailText(string(data), 64<<10))
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s 超时（15m，详细日志: %s）", ps, logPath)
	}
	if runErr != nil {
		return fmt.Errorf("%s: %w（详细日志: %s）", ps, runErr, logPath)
	}
	return nil
}

func runWithEnv(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		log.Printf("%s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// tailText 只保留末尾 n 字节，避免超长构建日志淹没安装器输出。
func tailText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// runTimeout 执行外部命令并限制最长运行时间，避免服务/卸载脚本卡住整个安装器。
func runTimeout(timeout time.Duration, name string, args ...string) error {
	log.Printf("执行（超时 %s）: %s %s", timeout, name, strings.Join(args, " "))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		log.Printf("%s", strings.TrimSpace(string(out)))
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s 超时（%s）", name, timeout)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func runQuiet(name string, args ...string) {
	runQuietTimeout(20*time.Second, name, args...)
}

func runQuietTimeout(timeout time.Duration, name string, args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
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
	if interactiveMode {
		fmt.Print("\n按回车键退出...")
		var dummy string
		_, _ = fmt.Scanln(&dummy)
	}
	os.Exit(1)
}
