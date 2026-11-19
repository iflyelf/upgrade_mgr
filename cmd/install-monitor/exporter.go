package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Windows Exporter 的默认配置随安装器发布，安装/更新不再依赖 install.ps1。
//
//go:embed templates/latest.yaml templates/legacy.yaml
var exporterConfigFS embed.FS

const (
	exporterConfigVersion = "4"
	exporterMetricsDir    = `C:\monitor\exporter\metrics`
	exporterFirewallRule  = "windows_exporter (TCP 16666)"
)

// installExporter 直接安装/更新 Windows Exporter。
//
// 流程：下载 PE → 停服务/结束进程 → 备份 → 写默认配置 → 替换 exe →
// 创建/更新服务（延迟自启 + 失败重启）→ 防火墙 → 启动；失败自动回滚。
// 全程不调用 PowerShell，避免 install.ps1 在部分系统上卡住。
func installExporter(action string) {
	arch := resolveArch(opts.Arch)
	flavor := "latest"
	if windowsMajorVersion() < 10 {
		flavor = "legacy"
	}
	asset := "windows-exporter-" + arch
	if flavor == "legacy" {
		asset += "-legacy"
	}
	binURL := opts.Server + "/api/external/binary/" + asset
	log.Printf("下载 Windows Exporter（%s / %s）: %s", flavor, action, binURL)

	tmp := filepath.Join(os.TempDir(), "windows_exporter-download.exe")
	defer os.Remove(tmp)
	if err := downloadToTimeout(binURL, tmp, 10*time.Minute, 2); err != nil {
		fatal("下载 Windows Exporter 失败（请在控制台「外部文件」为 %s 上传离线文件）: %v", asset, err)
	}
	if !isPEBinary(tmp) {
		fatal("下载内容不是有效的 Windows 可执行文件（%s），请检查外部文件缓存", asset)
	}

	stopExporter()
	if err := os.MkdirAll(exporterInstall, 0o755); err != nil {
		fatal("创建安装目录失败: %v", err)
	}
	if err := os.MkdirAll(exporterMetricsDir, 0o755); err != nil {
		fatal("创建 textfile 目录失败: %v", err)
	}
	writeExporterConfig(flavor)

	backedUp := false
	if fileExists(exporterExePath) {
		log.Printf("备份当前版本: %s.bak", exporterExePath)
		_ = os.Remove(exporterExePath + ".bak")
		if err := copyFile(exporterExePath, exporterExePath+".bak"); err != nil {
			log.Printf("WARN: 备份失败（继续安装）: %v", err)
		} else {
			backedUp = true
		}
	}

	if err := replaceExporterBinary(tmp); err != nil {
		rollbackExporter(backedUp)
		fatal("替换 Windows Exporter 失败: %v", err)
	}
	if err := configureExporterService(flavor); err != nil {
		rollbackExporter(backedUp)
		fatal("配置 Windows Exporter 服务失败: %v", err)
	}
	installExporterFirewall()
	if err := startExporter(); err != nil {
		log.Printf("WARN: Windows Exporter 启动失败: %v", err)
		rollbackExporter(backedUp)
		fatal("Windows Exporter 启动失败，已回滚: %v", err)
	}
	log.Printf("Windows Exporter %s 完成（%s）", action, flavor)
}

func rollbackExporter(backedUp bool) {
	log.Printf("回滚 Windows Exporter...")
	stopExporter()
	if backedUp {
		if err := copyFile(exporterExePath+".bak", exporterExePath); err != nil {
			log.Printf("WARN: 回滚旧版本失败: %v", err)
		}
	} else {
		// 全新安装失败：删除服务和残留 exe，避免留下不可用服务。
		runQuiet("sc", "delete", exporterService)
		_ = os.Remove(exporterExePath)
	}
	if err := startExporter(); err != nil {
		log.Printf("WARN: 回滚后仍无法启动: %v", err)
	}
}

func isPEBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < 1<<20 {
		return false
	}
	head := make([]byte, 2)
	if _, err := f.Read(head); err != nil {
		return false
	}
	return head[0] == 'M' && head[1] == 'Z'
}

func writeExporterConfig(flavor string) {
	path := filepath.Join(exporterInstall, "config.yaml")
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
		old := string(data)
		// 旧 install.ps1 生成的配置且通道不一致时，备份后重写；其它用户配置保留。
		if strings.Contains(old, "# windows_exporter 配置（由") &&
			!strings.Contains(old, "# flavor: "+flavor) {
			_ = os.WriteFile(path+".bak", data, 0o644)
			log.Printf("配置通道变化，旧配置已备份到 %s.bak", path)
		} else {
			log.Printf("保留已有配置: %s", path)
			return
		}
	}
	body, err := exporterConfigFS.ReadFile("templates/" + flavor + ".yaml")
	if err != nil {
		fatal("读取内置 Windows Exporter 配置失败: %v", err)
	}
	header := fmt.Sprintf("# windows_exporter 配置（由 install-windows-monitor 生成）\n"+
		"# config-version: %s\n# flavor: %s\n", exporterConfigVersion, flavor)
	if err := os.WriteFile(path, []byte(header+string(body)), 0o644); err != nil {
		fatal("写入 Windows Exporter 配置失败: %v", err)
	}
	log.Printf("已生成默认配置: %s（flavor=%s）", path, flavor)
}

// replaceExporterBinary 覆盖 exe；被服务/残留进程占用时停止并重试，最终改名让位。
func replaceExporterBinary(src string) error {
	var lastErr error
	for i := 1; i <= 5; i++ {
		if err := copyFile(src, exporterExePath); err == nil {
			return nil
		} else {
			lastErr = err
			log.Printf("覆盖 exe 失败（第 %d 次）: %v", i, err)
			stopExporter()
			time.Sleep(2 * time.Second)
		}
	}
	aside := fmt.Sprintf("%s.old-%d", exporterExePath, time.Now().Unix())
	log.Printf("exe 仍被占用，改名让位: %s", aside)
	if err := os.Rename(exporterExePath, aside); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%v；改名让位失败: %w", lastErr, err)
	}
	return copyFile(src, exporterExePath)
}

func exporterBinPath(flavor string) string {
	textfileFlag := "--collector.textfile.directories"
	if flavor == "legacy" {
		textfileFlag = "--collector.textfile.directory"
	}
	// 安装目录固定在 C:\monitor\exporter（无空格），这里不再给路径加引号：
	// sc.exe 对含引号/空格的 binPath 转义很敏感，直接传单行命令行更稳。
	return fmt.Sprintf(`%s --config.file=%s --web.listen-address=:%d %s=%s`,
		exporterExePath, filepath.Join(exporterInstall, "config.yaml"),
		exporterPort, textfileFlag, exporterMetricsDir)
}

func serviceExists(name string) bool {
	return exec.Command("sc", "query", name).Run() == nil
}

func configureExporterService(flavor string) error {
	binPath := exporterBinPath(flavor)
	if serviceExists(exporterService) {
		if err := runTimeout(30*time.Second, "sc", "config", exporterService,
			"binPath=", binPath, "start=", "auto"); err != nil {
			return err
		}
	} else {
		err := runTimeout(30*time.Second, "sc", "create", exporterService,
			"binPath=", binPath, "DisplayName=", "windows_exporter", "start=", "auto")
		if err != nil && serviceExists(exporterService) {
			// 并发/残留导致 create 失败但服务已存在：改为更新配置。
			err = runTimeout(30*time.Second, "sc", "config", exporterService,
				"binPath=", binPath, "start=", "auto")
		}
		if err != nil {
			return err
		}
	}
	// delayed-auto 在部分旧系统 sc 不支持；失败不阻断，依赖下面的注册表兜底。
	runQuiet("sc", "config", exporterService, "start=", "delayed-auto")
	runQuiet("sc", "description", exporterService,
		"windows_exporter - Prometheus metrics exporter for Windows")
	runQuiet("sc", "failure", exporterService, "reset=", "86400",
		"actions=", "restart/60000/restart/60000/restart/60000")
	// 部分旧系统 sc 不识别 delayed-auto，直接写注册表兜底。
	runQuiet("reg", "add", `HKLM\SYSTEM\CurrentControlSet\Services\`+exporterService,
		"/v", "DelayedAutostart", "/t", "REG_DWORD", "/d", "1", "/f")
	return nil
}

func installExporterFirewall() {
	rule := `name="` + exporterFirewallRule + `"`
	runQuiet("netsh", "advfirewall", "firewall", "delete", "rule", rule)
	runQuiet("netsh", "advfirewall", "firewall", "add", "rule", rule,
		"dir=in", "action=allow", "protocol=TCP", fmt.Sprintf("localport=%d", exporterPort))
}

func startExporter() error {
	// 服务已在运行时 sc 返回 1056，这里忽略，统一轮询状态。
	runQuiet("sc", "start", exporterService)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if exporterServiceState() == "RUNNING" {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("服务 %s 未在 30 秒内进入 RUNNING", exporterService)
}

func stopExporter() {
	if !serviceExists(exporterService) {
		runQuietTimeout(20*time.Second, "taskkill", "/F", "/IM", "windows_exporter.exe")
		return
	}
	runQuiet("sc", "stop", exporterService)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if exporterServiceState() == "STOPPED" {
			break
		}
		time.Sleep(1 * time.Second)
	}
	// 手工启动/上次失败遗留的 exporter 进程可能仍占用 exe。
	runQuietTimeout(20*time.Second, "taskkill", "/F", "/IM", "windows_exporter.exe")
}

func exporterServiceState() string {
	out, err := exec.Command("sc", "query", exporterService).CombinedOutput()
	if err != nil {
		return ""
	}
	text := strings.ToUpper(string(out))
	switch {
	case strings.Contains(text, "RUNNING"):
		return "RUNNING"
	case strings.Contains(text, "STOPPED"):
		return "STOPPED"
	default:
		return ""
	}
}
