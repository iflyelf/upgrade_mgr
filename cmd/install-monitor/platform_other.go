//go:build !windows

package main

// windowsMajorVersion 仅用于交叉编译检查；实际安装器只在 Windows 运行。
func windowsMajorVersion() int { return 10 }
