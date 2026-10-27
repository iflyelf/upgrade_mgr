package task

import (
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// 任务目标系统：空字符串表示无法判断，不按系统过滤主机。
const (
	targetOSLinux   = "linux"
	targetOSWindows = "windows"
)

var winDrivePath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// osLabel 返回展示用的系统名。
func osLabel(os string) string {
	if os == targetOSWindows {
		return "Windows"
	}
	return "Linux"
}

// interpreterOS 按解释器判断系统；无法判断返回空。
func interpreterOS(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	// Windows 路径在 Linux 上 filepath.Base 不认反斜杠，这里统一替换。
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(p, `\`, "/")))
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "powershell", "pwsh", "cmd":
		return targetOSWindows
	case "bash", "sh", "dash", "ksh", "zsh":
		return targetOSLinux
	}
	return ""
}

// scriptExtOS 按脚本地址扩展名判断系统；无法判断返回空。
func scriptExtOS(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	p := raw
	if u, err := url.Parse(raw); err == nil {
		p = u.Path
		// 经 proxy 代理时真实地址在 ?url= 参数里。
		if inner := u.Query().Get("url"); inner != "" {
			if iu, err := url.Parse(inner); err == nil {
				p = iu.Path
			}
		}
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".ps1", ".cmd", ".bat":
		return targetOSWindows
	case ".sh", ".bash":
		return targetOSLinux
	}
	return ""
}

// commandTargetOS 命令任务的目标系统：先看解释器，再看脚本是否带 shebang。
//
// 解释器留空且无 shebang 时无法判断（Agent 会按本机默认解释器执行），不过滤。
func commandTargetOS(spec protocol.CommandSpec) string {
	if os := interpreterOS(spec.Interpreter); os != "" {
		return os
	}
	if strings.HasPrefix(strings.TrimLeft(spec.Script, " \t\r\n\ufeff"), "#!") {
		return targetOSLinux
	}
	return ""
}

// componentTargetOS 组件任务的目标系统。
//
// 优先级：os_match 显式声明 > script_url 扩展名 > exec_path 解释器 > install_path 路径风格。
func componentTargetOS(c *component.Component) string {
	if c == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(c.OSMatch)) {
	case "windows", "win":
		return targetOSWindows
	case "linux":
		return targetOSLinux
	}
	if os := scriptExtOS(c.ScriptURL); os != "" {
		return os
	}
	if os := interpreterOS(c.ExecPath); os != "" {
		return os
	}
	p := strings.TrimSpace(c.InstallPath)
	switch {
	case winDrivePath.MatchString(p):
		return targetOSWindows
	case strings.HasPrefix(p, "/"):
		return targetOSLinux
	}
	return ""
}

// hostOS 主机系统：os_family=windows 为 Windows，其他非空值为 Linux，空值未知。
func hostOS(osFamily string) string {
	f := strings.ToLower(strings.TrimSpace(osFamily))
	switch f {
	case "":
		return ""
	case targetOSWindows:
		return targetOSWindows
	}
	return targetOSLinux
}

// osMismatch 主机系统已知且与任务目标系统不同时返回 true。
func osMismatch(target, osFamily string) bool {
	if target == "" {
		return false
	}
	h := hostOS(osFamily)
	return h != "" && h != target
}

// filterHostsByOS 按目标系统剔除不匹配的主机，返回保留的主机与剔除数量。
func filterHostsByOS(hosts []hostRow, target string) ([]hostRow, int) {
	if target == "" {
		return hosts, 0
	}
	kept := hosts[:0:0]
	skipped := 0
	for _, h := range hosts {
		if osMismatch(target, h.osFamily) {
			skipped++
			continue
		}
		kept = append(kept, h)
	}
	return kept, skipped
}
