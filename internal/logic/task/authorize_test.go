package task

import (
	"context"
	"errors"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// testAuthorizer 记录调用并返回预设错误。
type testAuthorizer struct {
	called bool
	args   []any
	err    error
}

func (a *testAuthorizer) authorize(ctx context.Context, actor *auth.User,
	componentID int64, name string) error {
	a.called = true
	a.args = []any{actor, componentID, name}
	return a.err
}

// TestCheckDeployPassesWhenNoAuthorizer 未注入时不限制（兼容测试/小规模部署）。
func TestCheckDeployPassesWhenNoAuthorizer(t *testing.T) {
	s := NewService(nil, nil, nil, nil)
	if err := s.checkDeploy(context.Background(), &auth.User{Username: "u"}, 1, "c"); err != nil {
		t.Fatalf("未注入 authorizer 应放行: %v", err)
	}
}

// TestCheckDeployCallsAuthorizer 注入后调用，并把错误透传。
func TestCheckDeployCallsAuthorizer(t *testing.T) {
	s := NewService(nil, nil, nil, nil)
	a := &testAuthorizer{err: errors.New("no deploy")}
	s.SetAuthorizer(a.authorize)
	actor := &auth.User{Username: "u"}
	err := s.checkDeploy(context.Background(), actor, 7, "demo")
	if !a.called || err == nil {
		t.Fatalf("应调用 authorizer 并返回错误: called=%v err=%v", a.called, err)
	}
	if a.args[1].(int64) != 7 || a.args[2].(string) != "demo" {
		t.Fatalf("参数不符: %v", a.args)
	}
}

// TestCheckHostGroups 主机分组范围：空/「*」放行，具体值按 app_code/biz_group 匹配。
func TestCheckHostGroups(t *testing.T) {
	s := NewService(nil, nil, nil, nil)
	actor := &auth.User{Username: "u"}
	hosts := []hostRow{
		{id: 1, ip: "10.0.0.1", appCode: "app1", bizGroup: "g1"},
		{id: 2, ip: "10.0.0.2", appCode: "app2", bizGroup: "g2"},
	}
	// 未注入 resolver：不校验
	if err := s.checkHostGroups(context.Background(), actor, "comp", hosts); err != nil {
		t.Fatalf("未注入应放行: %v", err)
	}

	s.SetHostGroupResolver(func(ctx context.Context, a *auth.User, comp string) (map[string]bool, error) {
		return map[string]bool{"app1": true}, nil
	})
	if err := s.checkHostGroups(context.Background(), actor, "comp", hosts); err == nil {
		t.Fatal("存在范围外主机时应拒绝")
	}
	// 全部在范围内
	if err := s.checkHostGroups(context.Background(), actor, "comp", hosts[:1]); err != nil {
		t.Fatalf("范围内应放行: %v", err)
	}
	// "*" 表示不限
	s.SetHostGroupResolver(func(ctx context.Context, a *auth.User, comp string) (map[string]bool, error) {
		return map[string]bool{"*": true}, nil
	})
	if err := s.checkHostGroups(context.Background(), actor, "comp", hosts); err != nil {
		t.Fatalf("* 应放行: %v", err)
	}
	// 空集合表示不限（未配置分组）
	s.SetHostGroupResolver(func(ctx context.Context, a *auth.User, comp string) (map[string]bool, error) {
		return nil, nil
	})
	if err := s.checkHostGroups(context.Background(), actor, "comp", hosts); err != nil {
		t.Fatalf("空集合应放行: %v", err)
	}
	// resolver 报错应透传
	s.SetHostGroupResolver(func(ctx context.Context, a *auth.User, comp string) (map[string]bool, error) {
		return nil, errors.New("boom")
	})
	if err := s.checkHostGroups(context.Background(), actor, "comp", hosts); err == nil {
		t.Fatal("resolver 错误应透传")
	}
}

// TestSetActorNotFromJSON actor 不能通过 JSON 注入（避免伪造身份）。
func TestSetActorNotFromJSON(t *testing.T) {
	req := CreateRequest{Kind: KindUpgrade, ComponentID: 1, Version: "v1"}
	if req.actor != nil {
		t.Fatal("初始 actor 应为 nil")
	}
	actor := &auth.User{Username: "alice"}
	req.SetActor(actor)
	if req.actor != actor {
		t.Fatal("SetActor 应写入")
	}
}
