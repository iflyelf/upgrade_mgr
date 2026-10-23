//go:build windows

package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentpath"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// powershellPath 返回系统自带 Windows PowerShell 的可执行文件路径。
//
// Server 2008 R2 起系统预装 PowerShell 2.0，优先用绝对路径，
// 避免服务进程 PATH 被裁剪后找不到 powershell.exe。
func powershellPath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "powershell.exe"
}

// shellPath 选择默认解释器：优先 Windows PowerShell，缺失时回退 cmd.exe。
func shellPath() string {
	if p := powershellPath(); p != "powershell.exe" {
		return p
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "cmd.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "cmd.exe"
}

// validateInterpreter 限定解释器白名单（PowerShell / cmd），防止执行任意程序。
func validateInterpreter(p string) error {
	switch strings.ToLower(imageName(p)) {
	case "powershell.exe", "powershell", "pwsh.exe", "pwsh", "cmd.exe", "cmd":
		return nil
	default:
		return fmt.Errorf("不支持的解释器 %q（仅允许 powershell/pwsh/cmd）", p)
	}
}

// imageName 取可执行文件名并去掉 .exe 之外的路径。
func imageName(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Base(strings.Trim(p, `"`))
}

// isCmdInterpreter 判断是否为 cmd.exe。
func isCmdInterpreter(p string) bool {
	switch strings.ToLower(imageName(p)) {
	case "cmd.exe", "cmd":
		return true
	}
	return false
}

// scriptExt 返回脚本扩展名。
//
// 已有 .ps1/.cmd/.bat 后缀时保持不变（运维上传的脚本按自身类型执行），
// 其余情况一律按 PowerShell 脚本处理。
func scriptExt(scriptPath string) string {
	switch strings.ToLower(filepath.Ext(scriptPath)) {
	case ".ps1":
		return ".ps1"
	case ".cmd":
		return ".cmd"
	case ".bat":
		return ".bat"
	default:
		return ".ps1"
	}
}

// prepareScriptPath 确保脚本路径带 PowerShell/cmd 可识别的扩展名。
//
// 平台制品下载地址常是 /api/artifacts/{id}/download，落盘成 artifact-1.bin；
// PowerShell 的 -File 只接受 .ps1，因此无扩展名时复制一份 .ps1。
func prepareScriptPath(scriptPath string) (string, func(), error) {
	switch strings.ToLower(filepath.Ext(scriptPath)) {
	case ".ps1", ".cmd", ".bat":
		return scriptPath, func() {}, nil
	case ".sh", ".bash", ".py":
		// 保留原扩展名，让 scriptArgv 给出「Windows 无法执行 Shell 脚本」的明确错误。
		return scriptPath, func() {}, nil
	}
	src, err := os.Open(scriptPath)
	if err != nil {
		return "", nil, fmt.Errorf("读取安装脚本失败: %w", err)
	}
	defer src.Close()
	dstPath := scriptPath + ".ps1"
	dst, err := os.Create(dstPath)
	if err != nil {
		return "", nil, fmt.Errorf("创建 PowerShell 脚本失败: %w", err)
	}
	if _, err := dst.ReadFrom(src); err != nil {
		dst.Close()
		_ = os.Remove(dstPath)
		return "", nil, fmt.Errorf("写入 PowerShell 脚本失败: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(dstPath)
		return "", nil, err
	}
	return dstPath, func() { _ = os.Remove(dstPath) }, nil
}

// commandScriptExt 一次性命令脚本的扩展名，随解释器而定。
func commandScriptExt(interp string) string {
	if isCmdInterpreter(interp) {
		return ".cmd"
	}
	return ".ps1"
}

// scriptArgv 构造脚本执行 argv。
//
// Windows 按脚本扩展名选择解释器：
//   - .cmd/.bat → cmd.exe /c
//   - .ps1 及其他 → powershell.exe -NoProfile -ExecutionPolicy Bypass -File
//
// exec_path 若显式填了 powershell/cmd 则优先尊重，保证组件配置可控。
func scriptArgv(comp protocol.ComponentSpec, scriptPath string) ([]string, error) {
	// 下载的脚本按扩展名判定；Windows 无法执行 POSIX Shell 脚本。
	ext := strings.ToLower(filepath.Ext(scriptPath))
	if ext == ".sh" || ext == ".bash" || ext == ".py" {
		return nil, fmt.Errorf("Windows 无法执行 %s 脚本，请改用 .ps1（PowerShell）或 .cmd/.bat 脚本", ext)
	}

	if p := strings.TrimSpace(comp.ExecPath); p != "" {
		if err := validateInterpreter(p); err == nil {
			return interpreterArgv(p, scriptPath, comp.ExecArgs), nil
		}
	}
	interp := shellPath()
	if ext == ".cmd" || ext == ".bat" {
		if !isCmdInterpreter(interp) {
			interp = `cmd.exe`
		}
		return interpreterArgv(interp, scriptPath, comp.ExecArgs), nil
	}
	if isCmdInterpreter(interp) {
		// 系统缺少 PowerShell（极老环境）时退回 cmd 无法执行 ps1，
		// 这里仍然选择 PowerShell 全路径，让失败原因明确。
		interp = powershellPath()
	}
	return interpreterArgv(interp, scriptPath, comp.ExecArgs), nil
}

// interpreterArgv 构造「解释器 + 脚本 + 参数」的命令行。
func interpreterArgv(interp, scriptPath string, args []string) []string {
	if isCmdInterpreter(interp) {
		return append([]string{interp, "/c", scriptPath}, args...)
	}
	argv := []string{interp, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath}
	return append(argv, args...)
}

// ttySupported Windows 无可用伪终端，忽略 TTY 选项。
func ttySupported() bool { return false }

// ttyCommand Windows 上退化为普通执行。
func ttyCommand(interp, scriptPath string, args []string) []string {
	return interpreterArgv(interp, scriptPath, args)
}

// denyPrefixes 是硬安全边界：拒绝替换/删除系统关键目录下的文件。
//
// Windows Server 2008 R2 ~ Windows 11 的系统目录、启动分区与 Agent 自身
// 数据目录一旦被覆盖会直接导致主机不可用或凭据泄露。
func denyPrefixes() []string {
	var out []string
	if root := os.Getenv("SystemRoot"); root != "" {
		out = append(out, root, filepath.Join(root, "System32"), filepath.Join(root, "SysWOW64"))
	} else {
		out = append(out, `C:\Windows`)
	}
	if pd := os.Getenv("ProgramData"); pd != "" {
		out = append(out, filepath.Join(pd, "Microsoft"))
	}
	out = append(out,
		filepath.Dir(agentpath.BinPath()), // Agent 自身安装目录，只允许自升级流程改写
		agentpath.DataDir(),               // state/凭据与自升级备份
	)
	return out
}

// allowInstallRoots 允许部署的根目录，可用 UPGRADE_AGENT_ALLOW_ROOTS 覆盖
// （分号或冒号分隔）。默认涵盖 Program Files、ProgramData 与常见自建目录。
func allowInstallRoots() []string {
	if v := os.Getenv("UPGRADE_AGENT_ALLOW_ROOTS"); v != "" {
		var out []string
		for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == ':' }) {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	var roots []string
	if v := os.Getenv("ProgramFiles"); v != "" {
		roots = append(roots, v)
	} else {
		roots = append(roots, `C:\Program Files`)
	}
	if v := os.Getenv("ProgramFiles(x86)"); v != "" {
		roots = append(roots, v)
	}
	if v := os.Getenv("ProgramData"); v != "" {
		roots = append(roots, v)
	} else {
		roots = append(roots, `C:\ProgramData`)
	}
	roots = append(roots, `C:\Tools`, `C:\Apps`, `C:\Upgrade`)
	return roots
}

// validateInstallPath 校验 install_path，防止越权替换/删除敏感文件。
//
// 校验顺序：
//  1. 必须是本地盘符绝对路径（拒绝 UNC 与 ..，防路径遍历）
//  2. 命中系统关键目录立即拒绝（硬边界）
//  3. 必须落在允许的部署根目录内
//  4. 校验解析符号链接/目录联接后的真实路径仍在允许范围内
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
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return fmt.Errorf("不支持 UNC 网络路径: %s", path)
	}
	absPath := filepath.Clean(path)

	// 2) 硬安全边界优先：系统目录命中即拒。
	if withinAnyDir(absPath, denyPrefixes()) {
		return fmt.Errorf("拒绝操作系统关键路径 %s", absPath)
	}

	// 3) 必须在允许的部署根目录内。
	if !withinAnyDir(absPath, allowInstallRoots()) {
		return fmt.Errorf("路径 %s 不在允许的部署目录内（可用 UPGRADE_AGENT_ALLOW_ROOTS 配置）", absPath)
	}

	// 4) 防目录联接/符号链接逃逸到系统目录。
	if realParent, err := filepath.EvalSymlinks(filepath.Dir(absPath)); err == nil {
		realTarget := filepath.Join(realParent, filepath.Base(absPath))
		if withinAnyDir(realTarget, denyPrefixes()) {
			return fmt.Errorf("目录联接指向系统关键路径 %s", realTarget)
		}
	}
	return nil
}

// withinAnyDir 判断 absPath 是否等于或位于某个根目录内（大小写不敏感）。
func withinAnyDir(absPath string, roots []string) bool {
	target := strings.TrimRight(filepath.Clean(absPath), `\`)
	lower := strings.ToLower(target)
	for _, root := range roots {
		r := strings.ToLower(strings.TrimRight(filepath.Clean(root), `\`))
		if r == "" {
			continue
		}
		if lower == r || strings.HasPrefix(lower, r+`\`) {
			return true
		}
	}
	return false
}
