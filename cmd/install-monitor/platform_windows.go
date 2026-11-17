//go:build windows

package main

import "golang.org/x/sys/windows"

// windowsMajorVersion 返回 NT 主版本号（10 = Windows 10 / Server 2016+）。
// 用于选择 windows-exporter 的 latest / legacy 资产。
func windowsMajorVersion() int {
	major, _, _ := windows.RtlGetNtVersionNumbers()
	return int(major)
}
