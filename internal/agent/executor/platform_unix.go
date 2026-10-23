//go:build !windows

package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// shellPath 选择目标机可用的 Shell：优先 bash，缺失时回退 sh。
func shellPath() string {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// validateInterpreter 限定解释器白名单（bash/sh），防止把任意二进制当成脚本执行器。
func validateInterpreter(p string) error {
	base := filepath.Base(strings.TrimSpace(p))
	switch base {
	case "bash", "sh", "dash", "ksh", "zsh":
		return nil
	default:
		return fmt.Errorf("不支持的解释器 %q（仅允许 bash/sh/dash/ksh/zsh）", p)
	}
}

// scriptExt 返回脚本扩展名。Unix 统一使用 .sh。
func scriptExt(scriptPath string) string { return ".sh" }

// scriptArgv 构造脚本执行 argv。
//
// exec + script_url 时组件可能仍填了 exec_path=/bin/bash，优先尊重它；
// 只接受 shell 解释器，避免被用来执行任意二进制。
func scriptArgv(comp protocol.ComponentSpec, scriptPath string) ([]string, error) {
	interp := shellPath()
	switch filepath.Base(strings.TrimSpace(comp.ExecPath)) {
	case "bash", "sh", "dash", "ksh", "zsh":
		interp = strings.TrimSpace(comp.ExecPath)
	}
	return append([]string{interp, scriptPath}, comp.ExecArgs...), nil
}

// prepareScriptPath Unix 不需要调整脚本路径。
func prepareScriptPath(scriptPath string) (string, func(), error) {
	return scriptPath, func() {}, nil
}

// commandScriptExt 返回一次性命令脚本的扩展名。
func commandScriptExt(interp string) string { return ".sh" }

// interpreterArgv 构造「解释器 + 脚本 + 参数」的命令行。
func interpreterArgv(interp, scriptPath string, args []string) []string {
	return append([]string{interp, scriptPath}, args...)
}

// ttySupported Unix 支持 script(1) 伪终端。
func ttySupported() bool { return true }

// ttyCommand 构造通过 script(1) 启动的命令，把脚本输出接入伪终端。
//
// script -q -c "<cmd>" /dev/null：quiet、不额外记录 typescript 文件。
// 命令字符串中的每个参数单独做 shell 单引号转义，避免注入。
//
// 若 script(1) 支持 -e/--return（util-linux 2.24+），一并启用：
// 否则 script 总是返回 0，子命令失败会被误判为成功。
func ttyCommand(interp, path string, args []string) []string {
	parts := make([]string, 0, len(args)+2)
	parts = append(parts, shellQuote(interp), shellQuote(path))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	cmd := strings.Join(parts, " ")
	argv := []string{"script"}
	if scriptSupportsReturn() {
		argv = append(argv, "-e")
	}
	return append(argv, "-q", "-c", cmd, "/dev/null")
}

var (
	scriptReturnOnce sync.Once
	scriptReturnOK   bool
)

// scriptSupportsReturn 探测 script(1) 是否支持 -e/--return。
// 老版本（如 CentOS 6 的 util-linux 2.17）不支持，探测失败时保持原行为。
func scriptSupportsReturn() bool {
	scriptReturnOnce.Do(func() {
		c := exec.Command("script", "-e", "-q", "-c", "exit 0", "/dev/null")
		scriptReturnOK = c.Run() == nil
	})
	return scriptReturnOK
}

// shellQuote 用单引号包裹参数，内部单引号按 POSIX 方式转义。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
