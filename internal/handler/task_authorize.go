package handler

import (
	"context"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/org"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TaskAuthorizer 组件级任务权限校验（由 svc 组装时注入 task.Service）。
//
// 实际规则在 org 包内实现（避免 svc → handler 的循环依赖）；
// 这里保留一个薄封装，便于 handler 层单测直接构造。
func TaskAuthorizer(s *svc.ServiceContext) func(context.Context, *auth.User, int64, string) error {
	return org.DeployAuthorizer(org.NewService(s.DB))
}

// TaskHostGroupResolver 主机分组范围解析（由 svc 组装时注入 task.Service）。
func TaskHostGroupResolver(s *svc.ServiceContext) func(context.Context, *auth.User, string) (map[string]bool, error) {
	return org.HostGroupResolver(org.NewService(s.DB))
}
