// Package osinfo 探测目标机的操作系统、架构、内核与 init 系统。
//
// 兼容范围：
//   - Linux：CentOS6（无 /etc/os-release，需回退 redhat-release）到 Ubuntu 26.04；
//   - Windows：Server 2008 R2 ~ Windows 11（amd64 / arm64）。
//
// 全部纯 Go 实现，CGO_ENABLED=0 可用；不引入第三方依赖，便于用旧版 Go 编译。
package osinfo

import (
	"runtime"
	"strings"
)

// Info 主机系统信息
type Info struct {
	OSFamily  string `json:"os_family"`  // rhel / debian / suse / alpine / linux / windows
	OSName    string `json:"os_name"`    // CentOS / Ubuntu / Windows Server 2019 ...
	OSVersion string `json:"os_version"` // 6.10 / 22.04 / 10.0.17763 ...
	Arch      string `json:"arch"`       // amd64 / arm64 / arm / 386
	Kernel    string `json:"kernel"`     // uname -r / Windows build
	Init      string `json:"init"`       // systemd / upstart / sysv / none / windows
}

// Detect 探测本机系统信息
func Detect() Info {
	i := Info{
		Arch: runtime.GOARCH,
	}
	i.OSFamily, i.OSName, i.OSVersion, i.Kernel, i.Init = detectPlatform()
	return i
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func containsAny(hay string, needles ...string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(hay, n) {
			return true
		}
	}
	return false
}
