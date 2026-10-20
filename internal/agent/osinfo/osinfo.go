// Package osinfo 探测目标机的操作系统、架构、内核与 init 系统。
//
// 兼容范围：CentOS6（无 /etc/os-release，需回退 redhat-release）到 Ubuntu 26.04。
// 全部纯 Go 实现，CGO_ENABLED=0 可用。
package osinfo

import (
	"bufio"
	"os"
	"runtime"
	"strings"
)

// Info 主机系统信息
type Info struct {
	OSFamily  string `json:"os_family"`  // rhel / debian / suse / alpine / linux
	OSName    string `json:"os_name"`    // CentOS / Ubuntu ...
	OSVersion string `json:"os_version"` // 6.10 / 22.04 ...
	Arch      string `json:"arch"`       // amd64 / arm64 / arm / 386
	Kernel    string `json:"kernel"`     // uname -r
	Init      string `json:"init"`       // systemd / upstart / sysv / none
}

// Detect 探测本机系统信息
func Detect() Info {
	i := Info{
		Arch:   runtime.GOARCH,
		Kernel: kernelRelease(),
		Init:   detectInit(),
	}
	i.OSFamily, i.OSName, i.OSVersion = detectOS()
	return i
}

// detectOS 依次尝试各发行版标识文件，保证 CentOS6 也能识别
func detectOS() (family, name, version string) {
	// 1) /etc/os-release（现代发行版）
	if f, err := os.Open("/etc/os-release"); err == nil {
		defer f.Close()
		kv := parseKV(f)
		name = firstNonEmpty(kv["NAME"], kv["PRETTY_NAME"])
		version = firstNonEmpty(kv["VERSION_ID"], kv["VERSION"])
		family = familyFromIDs(kv["ID"], kv["ID_LIKE"], name)
		if family != "" {
			return family, name, version
		}
	}

	// 2) /etc/redhat-release / centos-release（CentOS6/RHEL6 无 os-release）
	for _, p := range []string{"/etc/centos-release", "/etc/redhat-release"} {
		if b, err := os.ReadFile(p); err == nil {
			line := strings.TrimSpace(string(b))
			return "rhel", cutBefore(line, " release"), extractVersion(line)
		}
	}

	// 3) /etc/lsb-release（旧 Ubuntu/Debian 衍生）
	if f, err := os.Open("/etc/lsb-release"); err == nil {
		defer f.Close()
		kv := parseKV(f)
		if id := kv["DISTRIB_ID"]; id != "" {
			return "debian", id, kv["DISTRIB_RELEASE"]
		}
	}

	// 4) /etc/debian_version
	if b, err := os.ReadFile("/etc/debian_version"); err == nil {
		return "debian", "Debian", strings.TrimSpace(string(b))
	}

	// 5) 兜底
	return "linux", "Linux", ""
}

// familyFromIDs 由 ID / ID_LIKE / NAME 推断发行版族
func familyFromIDs(id, idLike, name string) string {
	hay := strings.ToLower(id + " " + idLike + " " + name)
	switch {
	case containsAny(hay, "rhel", "centos", "fedora", "rocky", "almalinux", "red hat", "kylin", "openeuler", "uos", "anolis"):
		return "rhel"
	case containsAny(hay, "debian", "ubuntu"):
		return "debian"
	case containsAny(hay, "suse", "sles", "opensuse"):
		return "suse"
	case containsAny(hay, "alpine"):
		return "alpine"
	case id != "":
		return "linux"
	}
	return ""
}

// detectInit 探测 init 系统：systemd > upstart > sysv > none
func detectInit() string {
	// systemd：/run/systemd/system 存在，或 /proc/1/comm == systemd
	if isDir("/run/systemd/system") {
		return "systemd"
	}
	if b, err := os.ReadFile("/proc/1/comm"); err == nil {
		switch strings.TrimSpace(string(b)) {
		case "systemd":
			return "systemd"
		case "init":
			// 可能是 upstart 或 sysv：upstart 提供 initctl
			if fileExists("/sbin/initctl") && isUpstart() {
				return "upstart"
			}
			if isDir("/etc/init.d") {
				return "sysv"
			}
		}
	}
	// 无 /proc 或容器 PID1 非 init
	if fileExists("/sbin/init") || isDir("/etc/init.d") {
		if fileExists("/sbin/initctl") && isUpstart() {
			return "upstart"
		}
		if isDir("/etc/init.d") {
			return "sysv"
		}
	}
	return "none"
}

// isUpstart 通过 initctl version 输出判断是否 upstart（避免误判 systemd 的兼容 initctl）
func isUpstart() bool {
	// CentOS6 的 initctl 属于 upstart；这里只做存在性 + init.d 存在性组合判断，
	// 真正版本判定放到 svcmgr 执行时兜底，避免此处 exec。
	return fileExists("/etc/init/rcS.conf") || isDir("/etc/init")
}

func kernelRelease() string {
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// ---- 辅助 ----

func parseKV(f *os.File) map[string]string {
	kv := make(map[string]string)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		kv[strings.TrimSpace(k)] = v
	}
	return kv
}

func extractVersion(s string) string {
	// 从 "CentOS release 6.10 (Final)" 提取 6.10
	fields := strings.Fields(s)
	for _, f := range fields {
		if len(f) > 0 && (f[0] >= '0' && f[0] <= '9') {
			return strings.TrimRight(f, ".")
		}
	}
	return ""
}

func cutBefore(s, sep string) string {
	if i := strings.Index(s, sep); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
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
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
