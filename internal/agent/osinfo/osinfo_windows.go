//go:build windows

package osinfo

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// detectPlatform 探测 Windows 版本与构建号。
//
// RtlGetVersion 不受应用兼容性清单影响，能拿到真实的 NT 版本；
// 产品名（Server 2019 / Windows 11 等）从注册表读取，版本号统一为
// 主.次.构建号（如 10.0.17763）。Init 固定为 windows（SCM 服务）。
func detectPlatform() (family, name, version, kernel, initType string) {
	major, minor, build := windows.RtlGetNtVersionNumbers()
	version = fmt.Sprintf("%d.%d.%d", major, minor, build)
	kernel = fmt.Sprintf("%d.%d.%d", major, minor, build)
	name = windowsProductName()
	if name == "" {
		name = windowsNameByVersion(major, minor, build)
	}
	return "windows", name, version, kernel, "windows"
}

// windowsProductName 读取 HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProductName。
// 读取失败返回空串，由版本号兜底命名。
func windowsProductName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("ProductName")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// windowsNameByVersion 在注册表不可用时按 NT 版本粗略命名。
func windowsNameByVersion(major, minor, build uint32) string {
	switch {
	case major == 6 && minor == 1:
		return "Windows 7 / Server 2008 R2"
	case major == 6 && minor == 2:
		return "Windows 8 / Server 2012"
	case major == 6 && minor == 3:
		return "Windows 8.1 / Server 2012 R2"
	case major == 10 && build >= 22000:
		return "Windows 11 / Server 2022+"
	case major == 10:
		return "Windows 10 / Server 2016+"
	default:
		return fmt.Sprintf("Windows %d.%d", major, minor)
	}
}
