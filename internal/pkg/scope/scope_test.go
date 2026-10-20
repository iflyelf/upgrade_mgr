package scope

import (
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

func TestForUser_NormalUserScoped(t *testing.T) {
	u := &auth.User{TenantID: 7, Roles: []string{auth.RoleUser}}
	s := ForUser(u)
	if s.CrossTenant {
		t.Fatal("普通用户不应跨租户")
	}
	if s.TenantID != 7 {
		t.Fatalf("租户应为 7，实际 %d", s.TenantID)
	}
	cond, args := s.Where("components", 1)
	if !strings.Contains(cond, "components.tenant_id = $1") {
		t.Fatalf("应生成租户过滤条件，实际 %q", cond)
	}
	if len(args) != 1 || args[0].(int64) != 7 {
		t.Fatalf("参数应为 [7]，实际 %v", args)
	}
	t.Logf("✅ 普通用户被限定在租户 %d: %s", s.TenantID, cond)
}

func TestForUser_SuperAdminCrossTenant(t *testing.T) {
	u := &auth.User{TenantID: 3, Roles: []string{auth.RoleSuperAdmin}}
	s := ForUser(u)
	if !s.CrossTenant {
		t.Fatal("superadmin 应可跨租户")
	}
	cond, args := s.Where("components", 1)
	if cond != "TRUE" || len(args) != 0 {
		t.Fatalf("superadmin 不应加过滤，实际 cond=%q args=%v", cond, args)
	}
	t.Logf("✅ superadmin 跨租户放行")
}

// TestForUser_NilUserFailsClosed 关键安全用例：
// 未认证/空用户必须 fail-close（限定到不存在的租户），绝不能放行全部数据。
func TestForUser_NilUserFailsClosed(t *testing.T) {
	s := ForUser(nil)
	if s.CrossTenant {
		t.Fatal("空用户绝不能跨租户")
	}
	if s.TenantID != -1 {
		t.Fatalf("空用户应限定到不存在的租户(-1)，实际 %d", s.TenantID)
	}
	cond, args := s.Where("hosts", 1)
	if !strings.Contains(cond, "tenant_id") {
		t.Fatalf("空用户仍须带租户过滤，实际 %q", cond)
	}
	if args[0].(int64) != -1 {
		t.Fatalf("应为 -1 以匹配空集，实际 %v", args[0])
	}
	t.Logf("✅ 空用户 fail-close：限定到 tenant_id=-1（空集）")
}

// TestForUser_AdminIsStillScoped 普通 admin 只是租户内管理员，不得跨租户。
func TestForUser_AdminIsStillScoped(t *testing.T) {
	u := &auth.User{TenantID: 5, IsAdmin: true, Roles: []string{auth.RoleAdmin}}
	s := ForUser(u)
	if s.CrossTenant {
		t.Fatal("租户内 admin 不应跨租户")
	}
	if s.TenantID != 5 {
		t.Fatalf("应限定租户 5，实际 %d", s.TenantID)
	}
	t.Logf("✅ 租户内 admin 仍被限定在租户 %d", s.TenantID)
}

func TestScope_PlaceholderOffset(t *testing.T) {
	s := Scope{TenantID: 9}
	cond, _ := s.Where("t", 3)
	if !strings.Contains(cond, "$3") {
		t.Fatalf("占位符应从 $3 开始，实际 %q", cond)
	}
	t.Logf("✅ 占位符偏移正确: %s", cond)
}
