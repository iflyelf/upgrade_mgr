package org

import (
	"context"
	"fmt"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// DeployAuthorizer 返回「组件级 deploy 权限」校验函数，供 task.Service 注入。
//
// 规则：
//   - 管理员（admin / superadmin）与匿名（内部调用）放行；
//   - 其他用户必须通过团队授权，在包含该组件的服务组上拥有 deploy 权限。
//
// 放在 org 包而不是 handler：svc 需要在构造时注入，若放 handler 会形成
// svc → handler → svc 的循环依赖。
func DeployAuthorizer(svc *Service) func(context.Context, *auth.User, int64, string) error {
	if svc == nil {
		return nil
	}
	return func(ctx context.Context, actor *auth.User, componentID int64, componentName string) error {
		if actor == nil || actor.IsAdmin || actor.IsSuperAdmin() || actor.HasRole(auth.RoleAdmin) {
			return nil
		}
		perms, _, err := svc.EffectivePermissionsWithHosts(ctx, actor.Username)
		if err != nil {
			return fmt.Errorf("校验组件权限失败: %w", err)
		}
		if set, ok := perms[componentName]; ok && set[PermDeploy] {
			return nil
		}
		return fmt.Errorf("无权对组件「%s」创建任务（需要 deploy 权限，请联系管理员在「人员组织 → 团队」授权）",
			componentName)
	}
}

// HostGroupResolver 返回「用户对组件的主机分组范围」解析函数，供 task.Service 注入。
//
// 返回集合为空或含 "*" 表示不限主机范围。
func HostGroupResolver(svc *Service) func(context.Context, *auth.User, string) (map[string]bool, error) {
	if svc == nil {
		return nil
	}
	return func(ctx context.Context, actor *auth.User, componentName string) (map[string]bool, error) {
		if actor == nil || actor.IsAdmin || actor.IsSuperAdmin() || actor.HasRole(auth.RoleAdmin) {
			return nil, nil
		}
		return svc.HostGroupsFor(ctx, actor.Username, componentName)
	}
}

// HostGroupsFor 返回用户对指定组件的主机分组范围（空集合或含 "*" 表示不限）。
func (s *Service) HostGroupsFor(ctx context.Context, username, componentName string) (map[string]bool, error) {
	_, hosts, err := s.EffectivePermissionsWithHosts(ctx, username)
	if err != nil {
		return nil, err
	}
	return hosts[componentName], nil
}
