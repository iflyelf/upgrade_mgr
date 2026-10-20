package handler

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// TestTenantIsolation_Components 验证租户隔离真实生效：
// A 租户用户看不到、改不了、删不掉 B 租户的组件；superadmin 可跨租户。
func TestTenantIsolation_Components(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	// 准备两个租户
	for _, id := range []int{901, 902} {
		db.Exec(`INSERT INTO tenants (id, name) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`,
			id, "iso_t"+string(rune('0'+id%10)))
	}
	defer db.Exec(`DELETE FROM tenants WHERE id IN (901,902)`)

	repo := component.NewRepo(db)
	scA := scope.New(901, false)
	scB := scope.New(902, false)
	scSuper := scope.New(901, true)

	db.Exec(`DELETE FROM components WHERE name IN ('iso_a','iso_b')`)
	defer db.Exec(`DELETE FROM components WHERE name IN ('iso_a','iso_b')`)

	idA, err := repo.Create(ctx, scA, &component.Component{Name: "iso_a", DeployMode: "replace"})
	if err != nil {
		t.Fatalf("A 建组件失败: %v", err)
	}
	idB, err := repo.Create(ctx, scB, &component.Component{Name: "iso_b", DeployMode: "replace"})
	if err != nil {
		t.Fatalf("B 建组件失败: %v", err)
	}

	// 1) 列表隔离
	listA, _ := repo.List(ctx, scA)
	for _, c := range listA {
		if c.Name == "iso_b" {
			t.Fatal("❌ 租户 A 看到了租户 B 的组件（隔离失效）")
		}
	}
	t.Log("✅ 列表隔离生效：A 看不到 B 的组件")

	// 2) 跨租户读取必须失败
	if _, err := repo.Get(ctx, scA, idB); err == nil {
		t.Fatal("❌ 租户 A 成功读取了租户 B 的组件（越权）")
	}
	t.Log("✅ 跨租户读取被拒绝")

	// 3) 跨租户修改必须失败
	err = repo.Update(ctx, scA, &component.Component{ID: idB, DisplayName: "hacked", DeployMode: "replace"})
	if err == nil {
		t.Fatal("❌ 租户 A 成功修改了租户 B 的组件（越权）")
	}
	var name string
	db.QueryRow(`SELECT COALESCE(display_name,'') FROM components WHERE id=$1`, idB).Scan(&name)
	if name == "hacked" {
		t.Fatal("❌ 租户 B 的数据被篡改")
	}
	t.Log("✅ 跨租户修改被拒绝，数据未被篡改")

	// 4) 跨租户删除必须失败
	if err := repo.Delete(ctx, scA, idB); err == nil {
		t.Fatal("❌ 租户 A 成功删除了租户 B 的组件（越权）")
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(1) FROM components WHERE id=$1`, idB).Scan(&cnt)
	if cnt != 1 {
		t.Fatal("❌ 租户 B 的组件被删除")
	}
	t.Log("✅ 跨租户删除被拒绝，数据仍在")

	// 5) superadmin 可跨租户
	if _, err := repo.Get(ctx, scSuper, idB); err != nil {
		t.Fatalf("❌ superadmin 应可跨租户读取，实际失败: %v", err)
	}
	t.Log("✅ superadmin 跨租户读取正常")

	// 6) 本租户操作正常
	if _, err := repo.Get(ctx, scA, idA); err != nil {
		t.Fatalf("❌ 本租户读取应成功: %v", err)
	}
	t.Log("✅ 本租户操作正常")
}

// TestReqScope_UnauthenticatedFailsClosed 未认证请求必须 fail-close 到空集。
func TestReqScope_UnauthenticatedFailsClosed(t *testing.T) {
	s := scope.ForUser(nil)
	if s.CrossTenant {
		t.Fatal("❌ 未认证用户不能跨租户")
	}
	cond, args := s.Where("", 1)
	if cond == "TRUE" {
		t.Fatal("❌ 未认证用户不能返回恒真条件")
	}
	if len(args) != 1 || args[0].(int64) != -1 {
		t.Fatalf("❌ 应限定到空集 tenant_id=-1，实际 %v", args)
	}
	t.Log("✅ 未认证 fail-close 到空集")
}

// TestScopeFromRealUser 验证从真实 auth.User 推导作用域的正确性。
func TestScopeFromRealUser(t *testing.T) {
	normal := &auth.User{TenantID: 5, Roles: []string{auth.RoleUser}}
	if scope.ForUser(normal).CrossTenant {
		t.Fatal("❌ 普通用户不应跨租户")
	}
	admin := &auth.User{TenantID: 5, Roles: []string{auth.RoleAdmin}}
	if scope.ForUser(admin).CrossTenant {
		t.Fatal("❌ 租户内 admin 不应跨租户")
	}
	super := &auth.User{TenantID: 5, Roles: []string{auth.RoleSuperAdmin}}
	if !scope.ForUser(super).CrossTenant {
		t.Fatal("❌ superadmin 应可跨租户")
	}
	t.Log("✅ 角色→作用域映射正确")
}
