package task

import (
	"context"
	"errors"
	"testing"
)

// TestOwnerOnlyFor 团队策略优先于全局开关；查询失败保守回退全局。
func TestOwnerOnlyFor(t *testing.T) {
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
		name     string
		global   bool
		policy   string
		polErr   error
		wantOnly bool
	}{
		{"未配置解析器-全局关", false, "", nil, false},
		{"未配置解析器-全局开", true, "", nil, true},
		{"团队 on 覆盖全局关", false, "on", nil, true},
		{"团队 on 覆盖全局开", true, "on", nil, true},
		{"团队 off 覆盖全局开", true, "off", nil, false},
		{"团队 off 覆盖全局关", false, "off", nil, false},
		{"团队 inherit 跟随全局开", true, "inherit", nil, true},
		{"团队 inherit 跟随全局关", false, "inherit", nil, false},
		{"未知策略回退全局", true, "bogus", nil, true},
		{"查询失败回退全局", true, "", errors.New("db down"), true},
		{"查询失败回退全局关", false, "", errors.New("db down"), false},
	}
	for _, c := range cases {
		s := newSvc(c.global, c.policy, c.polErr)
		if got := s.ownerOnlyFor(ctx, "alice"); got != c.wantOnly {
			t.Fatalf("%s: got %v want %v", c.name, got, c.wantOnly)
		}
	}
}
