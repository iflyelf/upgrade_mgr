package rbac

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// openDB 连接测试库；未配置则跳过。
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// seedTenant 建临时租户，返回 id 与清理函数。
func seedTenant(t *testing.T, db *sql.DB, name string) (int64, func()) {
	t.Helper()
	var id int64
	err := db.QueryRow(
		`INSERT INTO tenants (name, display_name, status) VALUES ($1,$1,'active') RETURNING id`,
		name).Scan(&id)
	if err != nil {
		t.Fatalf("建租户失败: %v", err)
	}
	return id, func() { db.Exec(`DELETE FROM tenants WHERE id=$1`, id) }
}

// TestCreateUser_ScopedToOwnTenant 租户管理员只能在本租户建用户。
func TestCreateUser_ScopedToOwnTenant(t *testing.T) {
	db := openDB(t)
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	svc := NewService(db)
	ctx := context.Background()

	sfx := time.Now().UnixNano()
	tidA, clA := seedTenant(t, db, fmt.Sprintf("rbac_a_%d", sfx))
	defer clA()
	tidB, clB := seedTenant(t, db, fmt.Sprintf("rbac_b_%d", sfx))
	defer clB()

	adminA := &auth.User{Username: "adminA", TenantID: tidA, Roles: []string{auth.RoleAdmin}}

	// 本租户创建：允许
	uname := fmt.Sprintf("u_a_%d", sfx)
	id, err := svc.Create(ctx, adminA, CreateRequest{
		Username: uname, Password: "pw123456", Roles: []string{auth.RoleUser},
	})
	if err != nil {
		t.Fatalf("本租户创建应成功: %v", err)
	}
	defer db.Exec(`DELETE FROM local_users WHERE id=$1`, id)

	var gotTenant int64
	db.QueryRow(`SELECT tenant_id FROM local_users WHERE id=$1`, id).Scan(&gotTenant)
	if gotTenant != tidA {
		t.Fatalf("新用户应归属租户 %d，实际 %d", tidA, gotTenant)
	}
	t.Logf("✅ 本租户建用户成功并归属正确: tenant=%d", gotTenant)

	// 跨租户创建：必须拒绝
	_, err = svc.Create(ctx, adminA, CreateRequest{
		Username: fmt.Sprintf("u_x_%d", sfx), Password: "pw123456",
		TenantID: tidB, Roles: []string{auth.RoleUser},
	})
	if err == nil {
		db.Exec(`DELETE FROM local_users WHERE username=$1`, fmt.Sprintf("u_x_%d", sfx))
		t.Fatal("跨租户创建用户必须被拒绝")
	}
	t.Logf("✅ 跨租户建用户被拒绝: %v", err)
}

// TestCreateUser_CannotGrantSuperadmin 关键安全用例：
// 租户管理员不得授予 superadmin，否则可自我提权到跨租户。
func TestCreateUser_CannotGrantSuperadmin(t *testing.T) {
	db := openDB(t)
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	svc := NewService(db)
	ctx := context.Background()

	sfx := time.Now().UnixNano()
	tid, cl := seedTenant(t, db, fmt.Sprintf("rbac_esc_%d", sfx))
	defer cl()

	adminA := &auth.User{Username: "adminA", TenantID: tid, Roles: []string{auth.RoleAdmin}}
	uname := fmt.Sprintf("u_esc_%d", sfx)

	_, err := svc.Create(ctx, adminA, CreateRequest{
		Username: uname, Password: "pw123456",
		Roles: []string{auth.RoleSuperAdmin},
	})
	if err == nil {
		db.Exec(`DELETE FROM local_users WHERE username=$1`, uname)
		t.Fatal("租户管理员授予 superadmin 必须被拒绝（防自我提权）")
	}
	t.Logf("✅ 提权尝试被拒绝: %v", err)
}

// TestSetTenantRoles_CannotEscalate 已存在用户也不能被提权为 superadmin。
func TestSetTenantRoles_CannotEscalate(t *testing.T) {
	db := openDB(t)
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	svc := NewService(db)
	ctx := context.Background()

	sfx := time.Now().UnixNano()
	tid, cl := seedTenant(t, db, fmt.Sprintf("rbac_up_%d", sfx))
	defer cl()

	adminA := &auth.User{Username: "adminA", TenantID: tid, Roles: []string{auth.RoleAdmin}}
	uname := fmt.Sprintf("u_up_%d", sfx)
	id, err := svc.Create(ctx, adminA, CreateRequest{
		Username: uname, Password: "pw123456", Roles: []string{auth.RoleUser},
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	defer db.Exec(`DELETE FROM local_users WHERE id=$1`, id)

	if err := svc.SetTenantRoles(ctx, adminA, uname, tid, []string{auth.RoleSuperAdmin}); err == nil {
		t.Fatal("提权为 superadmin 必须被拒绝")
	}
	t.Log("✅ 存量用户提权被拒绝")

	// 合法调整：允许
	if err := svc.SetTenantRoles(ctx, adminA, uname, tid, []string{auth.RoleAdmin}); err != nil {
		t.Fatalf("本租户合法调整应成功: %v", err)
	}
	var isAdmin bool
	db.QueryRow(`SELECT is_admin FROM local_users WHERE id=$1`, id).Scan(&isAdmin)
	if !isAdmin {
		t.Fatal("授予 admin 后 is_admin 应同步为 true")
	}
	t.Log("✅ 合法角色调整生效且 is_admin 同步")
}

// TestList_TenantScoped 列表按租户隔离，superadmin 可跨租户。
func TestList_TenantScoped(t *testing.T) {
	db := openDB(t)
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	svc := NewService(db)
	ctx := context.Background()

	sfx := time.Now().UnixNano()
	tidA, clA := seedTenant(t, db, fmt.Sprintf("rbac_la_%d", sfx))
	defer clA()
	tidB, clB := seedTenant(t, db, fmt.Sprintf("rbac_lb_%d", sfx))
	defer clB()

	su := &auth.User{Username: "root", TenantID: tidA, Roles: []string{auth.RoleSuperAdmin}}
	nameA := fmt.Sprintf("la_%d", sfx)
	nameB := fmt.Sprintf("lb_%d", sfx)

	idA, err := svc.Create(ctx, su, CreateRequest{Username: nameA, Password: "pw123456", TenantID: tidA, Roles: []string{auth.RoleUser}})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM local_users WHERE id=$1`, idA)
	idB, err := svc.Create(ctx, su, CreateRequest{Username: nameB, Password: "pw123456", TenantID: tidB, Roles: []string{auth.RoleUser}})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM local_users WHERE id=$1`, idB)

	// 租户 A 管理员只应看到 A 的用户
	adminA := &auth.User{Username: "adminA", TenantID: tidA, Roles: []string{auth.RoleAdmin}}
	listA, err := svc.List(ctx, adminA)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range listA {
		if u.Username == nameB {
			t.Fatal("租户 A 不应看到租户 B 的用户")
		}
	}
	t.Logf("✅ 用户列表按租户隔离（A 可见 %d 人，不含 B）", len(listA))

	// superadmin 应能看到两者
	listAll, err := svc.List(ctx, su)
	if err != nil {
		t.Fatal(err)
	}
	seenA, seenB := false, false
	for _, u := range listAll {
		if u.Username == nameA {
			seenA = true
		}
		if u.Username == nameB {
			seenB = true
		}
	}
	if !seenA || !seenB {
		t.Fatalf("superadmin 应看到全部用户，seenA=%v seenB=%v", seenA, seenB)
	}
	t.Log("✅ superadmin 跨租户可见")
}

// TestList_NilActorFailsClosed 身份缺失必须返回空集，不得泄露全部用户。
func TestList_NilActorFailsClosed(t *testing.T) {
	db := openDB(t)
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	svc := NewService(db)

	list, err := svc.List(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("身份缺失必须返回空集，实际 %d 条", len(list))
	}
	t.Log("✅ 身份缺失 fail-close 返回空集")
}

// TestSetDisabled 禁用后无法再登录（disabled 生效）。
func TestSetDisabled(t *testing.T) {
	db := openDB(t)
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	svc := NewService(db)
	ctx := context.Background()

	sfx := time.Now().UnixNano()
	tid, cl := seedTenant(t, db, fmt.Sprintf("rbac_dis_%d", sfx))
	defer cl()

	adminA := &auth.User{Username: "adminA", TenantID: tid, Roles: []string{auth.RoleAdmin}}
	uname := fmt.Sprintf("u_dis_%d", sfx)
	id, err := svc.Create(ctx, adminA, CreateRequest{Username: uname, Password: "pw123456", Roles: []string{auth.RoleUser}})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM local_users WHERE id=$1`, id)

	if err := svc.SetDisabled(ctx, adminA, uname, true); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}
	var disabled bool
	db.QueryRow(`SELECT disabled FROM local_users WHERE id=$1`, id).Scan(&disabled)
	if !disabled {
		t.Fatal("disabled 应为 true")
	}
	t.Log("✅ 用户禁用生效")

	// 禁用用户无法通过 builtin 登录
	b := auth.NewBuiltin(db, "test-secret-at-least-16-bytes", false)
	if _, _, err := b.Login(ctx, uname, "pw123456"); err == nil {
		t.Fatal("已禁用用户必须无法登录")
	}
	t.Log("✅ 已禁用用户无法登录")
}

// TestCreateRejectsWeakPassword 管理员创建用户同样执行密码策略（≥8 位）。
func TestCreateRejectsWeakPassword(t *testing.T) {
	db := openDB(t)
	t.Cleanup(func() { db.Close() })
	svc := NewService(db)
	ctx := context.Background()
	sfx := time.Now().UnixNano()
	tid, cl := seedTenant(t, db, fmt.Sprintf("rbac_pw_%d", sfx))
	defer cl()

	admin := &auth.User{Username: "adminPW", TenantID: tid, Roles: []string{auth.RoleAdmin}}
	uname := fmt.Sprintf("weak_%d", sfx)
	if _, err := svc.Create(ctx, admin, CreateRequest{
		Username: uname, Password: "short", Roles: []string{auth.RoleUser},
	}); err == nil {
		t.Fatal("弱密码应被拒绝")
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM local_users WHERE username=$1`, uname) })

	if _, err := svc.Create(ctx, admin, CreateRequest{
		Username: uname, Password: "good-password-1", Roles: []string{auth.RoleUser},
	}); err != nil {
		t.Fatalf("合法密码应通过: %v", err)
	}
}

// TestTenantAdminCannotModifyAdminAccounts 租户管理员不能降级或禁用同租户的
// superadmin/admin，否则可借此提升权限或瘫痪管理员账号。
func TestTenantAdminCannotModifyAdminAccounts(t *testing.T) {
	db := openDB(t)
	t.Cleanup(func() { db.Close() })
	svc := NewService(db)
	ctx := context.Background()

	sfx := time.Now().UnixNano()
	tid, cl := seedTenant(t, db, fmt.Sprintf("rbac_admin_%d", sfx))
	defer cl()

	target := fmt.Sprintf("admin_target_%d", sfx)
	if _, err := db.Exec(`
		INSERT INTO local_users (username, password_hash, display_name, is_admin, tenant_id, roles)
		VALUES ($1,'x','target',TRUE,$2,ARRAY['admin','superadmin'])`,
		target, tid); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM local_users WHERE username=$1`, target)

	tenantAdmin := &auth.User{Username: "tenant_admin", TenantID: tid, Roles: []string{auth.RoleAdmin}}

	if err := svc.SetTenantRoles(ctx, tenantAdmin, target, tid, []string{auth.RoleUser}); err == nil {
		t.Fatal("租户管理员降级 superadmin 必须被拒绝")
	}
	var isAdmin bool
	var rolesText string
	if err := db.QueryRow(`SELECT is_admin, COALESCE(roles::text,'{}') FROM local_users WHERE username=$1`,
		target).Scan(&isAdmin, &rolesText); err != nil {
		t.Fatal(err)
	}
	if !isAdmin || !strings.Contains(rolesText, "superadmin") {
		t.Fatalf("superadmin 被非法降级: is_admin=%v roles=%s", isAdmin, rolesText)
	}

	if err := svc.SetDisabled(ctx, tenantAdmin, target, true); err == nil {
		t.Fatal("租户管理员禁用 superadmin 必须被拒绝")
	}
	var disabled bool
	if err := db.QueryRow(`SELECT disabled FROM local_users WHERE username=$1`, target).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled {
		t.Fatal("superadmin 被非法禁用")
	}
}
