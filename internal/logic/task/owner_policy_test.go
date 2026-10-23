package task

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// TestOwnerModeFor 团队策略优先于全局开关；查询失败保守回退全局。
func TestOwnerModeFor(t *testing.T) {
	ctx := context.Background()
	newSvc := func(global bool, policy string, policyErr error) *Service {
		s := &Service{}
		s.SetOwnerOnlyPolicy(func() bool { return global })
		if policy != "" || policyErr != nil {
			s.SetOwnerPolicyResolver(func(context.Context, string) (string, error) {
				return policy, policyErr
			})
		}
		return s
	}

	cases := []struct {
		name   string
		global bool
		policy string
		polErr error
		want   string
	}{
		{"未配置解析器-全局关", false, "", nil, "inherit"},
		{"未配置解析器-全局开", true, "", nil, "on"},
		{"团队 on 覆盖全局关", false, "on", nil, "on"},
		{"团队 on 覆盖全局开", true, "on", nil, "on"},
		{"团队 off 覆盖全局开", true, "off", nil, "off"},
		{"团队 off 覆盖全局关", false, "off", nil, "off"},
		{"团队 inherit 跟随全局开", true, "inherit", nil, "on"},
		{"团队 inherit 跟随全局关", false, "inherit", nil, "inherit"},
		{"未知策略回退全局", true, "bogus", nil, "on"},
		{"查询失败回退全局", true, "", errors.New("db down"), "on"},
		{"查询失败回退全局关", false, "", errors.New("db down"), "inherit"},
	}
	for _, c := range cases {
		s := newSvc(c.global, c.policy, c.polErr)
		if got := s.ownerModeFor(ctx, "alice"); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestOwnerShareFallback 策略 off 且缺少共享校验器时保守拒绝。
func TestOwnerShareFallback(t *testing.T) {
	s := &Service{}
	s.SetOwnerOnlyPolicy(func() bool { return false })
	s.SetOwnerPolicyResolver(func(context.Context, string) (string, error) { return "off", nil })
	called := false
	s.SetOwnerShareChecker(func(context.Context, string, string) (bool, error) {
		called = true
		return false, nil
	})
	if got := s.ownerModeFor(context.Background(), "alice"); got != "off" {
		t.Fatalf("mode=%q", got)
	}
	if called {
		t.Fatal("ownerModeFor 不应触发共享校验")
	}
}

// TestNonAdminCannotCreateCommand 非管理员不能创建/预览命令任务。
func TestNonAdminCannotCreateCommand(t *testing.T) {
	s := &Service{allowExecMode: func() bool { return true }}

	bob := &auth.User{Username: "bob", TenantID: 1}
	req := CreateRequest{Kind: KindCommand, Name: "x"}
	req.SetActor(bob)

	if _, err := s.Create(context.Background(), scope.New(1, false), req); err == nil {
		t.Fatal("非管理员创建命令任务应被拒绝")
	}
	if _, err := s.Preview(context.Background(), scope.New(1, false), req); err == nil {
		t.Fatal("非管理员预览命令任务应被拒绝")
	}

	// 管理员可通过命令任务的权限检查（后续因缺 DB 失败，但错误不应是权限问题）
	admin := &auth.User{Username: "admin", TenantID: 1, IsAdmin: true}
	req.SetActor(admin)
	_, err := s.Create(context.Background(), scope.New(1, false), req)
	if err == nil || strings.Contains(err.Error(), "仅管理员") {
		t.Fatalf("管理员不应被权限拦截: %v", err)
	}
}
