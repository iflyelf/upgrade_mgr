//go:build !windows

// Package winsvc 在非 Windows 平台上是空实现，方便 cmd/agent 无构建标签地调用。
package winsvc

import "context"

// RunIfService 非 Windows 平台始终返回 false。
func RunIfService(name string, run func(ctx context.Context) error) (bool, error) {
	return false, nil
}
