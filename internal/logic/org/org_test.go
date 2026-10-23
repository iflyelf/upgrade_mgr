package org

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// TestNormalizePerms 权限白名单与去重。
func TestNormalizePerms(t *testing.T) {
	ok, err := normalizePerms([]string{"read", "WRITE", " read ", ""})
	if err != nil {
		t.Fatalf("应通过: %v", err)
	}
	if len(ok) != 2 || ok[0] != "read" || ok[1] != "write" {
		t.Fatalf("规范化失败: %v", ok)
	}
	if _, err := normalizePerms([]string{"root"}); err == nil {
		t.Fatal("非法权限应报错")
	}
}

// TestRoleCRUD 角色增删改查；被团队授权引用时拒绝删除。
func TestRoleCRUD(t *testing.T) {
	db := setupDB(t)
	s := NewService(db)
	ctx := context.Background()

	name := "role-" + t.Name()
	id, err := s.CreateRole(ctx, name, "", "测试角色", []string{PermRead, PermDeploy})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM roles WHERE id=$1`, id) })

	list, err := s.ListRoles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range list {
		if r.ID == id {
			found = true
			if len(r.Permissions) != 2 {
				t.Fatalf("权限不符: %v", r.Permissions)
			}
		}
	}
	if !found {
		t.Fatal("新建角色未出现在列表中")
	}

	if err := s.UpdateRole(ctx, id, name+"-new", "改后", []string{PermRead}); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	// 重复名称
	if _, err := s.CreateRole(ctx, name+"-new", "", "", nil); err == nil {
		t.Fatal("重复角色名应报错")
	}

	// 被团队引用时不可删
	teamID, err := s.CreateTeam(ctx, "team-"+t.Name(), "", "", nil, "tester")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	groupID, err := s.CreateGroup(ctx, "group-"+t.Name(), "", "", nil, "tester")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM teams WHERE id=$1`, teamID)
		db.Exec(`DELETE FROM service_groups WHERE id=$1`, groupID)
	})
	if err := s.SetGroupPermission(ctx, teamID, groupID, nil, []int64{id}, []string{"*"}); err != nil {
		t.Fatalf("SetGroupPermission: %v", err)
	}
	if err := s.DeleteRole(ctx, id); err == nil {
		t.Fatal("被引用的角色应拒绝删除")
	}
	// 解除引用后可删
	if err := s.DeleteGroupPermission(ctx, teamID, groupID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRole(ctx, id); err != nil {
		t.Fatalf("解除引用后应可删: %v", err)
	}
}

// TestTeamMembers 团队成员增删。
func TestTeamMembers(t *testing.T) {
	db := setupDB(t)
	s := NewService(db)
	ctx := context.Background()

	teamID, err := s.CreateTeam(ctx, "team-mem-"+t.Name(), "", "", []string{"u1", "u2"}, "tester")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM teams WHERE id=$1`, teamID) })

	members, err := s.ListMembers(ctx, teamID)
	if err != nil || len(members) != 2 {
		t.Fatalf("成员数不符: %v err=%v", members, err)
	}
	// 重复添加应幂等
	if err := s.AddMembers(ctx, teamID, []string{"u1", "u3"}); err != nil {
		t.Fatal(err)
	}
	members, _ = s.ListMembers(ctx, teamID)
	if len(members) != 3 {
		t.Fatalf("添加后应为 3，实际 %d", len(members))
	}
	if err := s.RemoveMember(ctx, teamID, "u2"); err != nil {
		t.Fatal(err)
	}
	members, _ = s.ListMembers(ctx, teamID)
	if len(members) != 2 {
		t.Fatalf("删除后应为 2，实际 %d", len(members))
	}
	// 成员记录随团队级联删除
	if err := s.DeleteTeam(ctx, teamID); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM team_members WHERE team_id=$1`, teamID).Scan(&n)
	if n != 0 {
		t.Fatalf("团队删除后成员应级联清理，剩余 %d", n)
	}
}

// TestGroupsAndPermissions 服务组组件集合与授权校验。
func TestGroupsAndPermissions(t *testing.T) {
	db := setupDB(t)
	s := NewService(db)
	ctx := context.Background()

	// 建两个组件用于组
	var comp1 string
	sfx := t.Name()
	if err := db.QueryRow(`
		INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING name`,
		"comp-a-"+sfx).Scan(&comp1); err != nil {
		t.Fatalf("建组件失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM components WHERE name=$1`, comp1) })
	var comp2 string
	if err := db.QueryRow(`
		INSERT INTO components (name, deploy_mode) VALUES ($1,'replace') RETURNING name`,
		"comp-b-"+sfx).Scan(&comp2); err != nil {
		t.Fatalf("建组件失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM components WHERE name=$1`, comp2) })

	var c1, c2 int64
	db.QueryRow(`SELECT id FROM components WHERE name=$1`, comp1).Scan(&c1)
	db.QueryRow(`SELECT id FROM components WHERE name=$1`, comp2).Scan(&c2)

	groupID, err := s.CreateGroup(ctx, "grp-"+sfx, "", "测试组", []int64{c1, c2}, "tester")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM service_groups WHERE id=$1`, groupID) })

	groups, err := s.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var g *Group
	for i := range groups {
		if groups[i].ID == groupID {
			g = &groups[i]
		}
	}
	if g == nil || len(g.ComponentIDs) != 2 {
		t.Fatalf("组内组件不符: %+v", g)
	}

	teamID, err := s.CreateTeam(ctx, "team-grp-"+sfx, "", "", []string{"alice"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM teams WHERE id=$1`, teamID) })

	// 授权：只给 comp1 的 read
	if err := s.SetGroupPermission(ctx, teamID, groupID,
		[]string{PermRead}, nil, []string{comp1}); err != nil {
		t.Fatalf("SetGroupPermission: %v", err)
	}
	perms, err := s.EffectivePermissions(ctx, "alice")
	if err != nil {
		t.Fatalf("EffectivePermissions: %v", err)
	}
	if !perms[comp1][PermRead] {
		t.Fatalf("alice 应对 %s 有 read 权限: %v", comp1, perms)
	}
	if perms[comp2][PermRead] {
		t.Fatalf("未授权的组件不应有权限: %v", perms)
	}

	// 改成 "*" + 角色权限并集
	roleID, err := s.CreateRole(ctx, "role-perm-"+sfx, "", "", []string{PermDeploy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM roles WHERE id=$1`, roleID) })
	if err := s.SetGroupPermission(ctx, teamID, groupID, nil, []int64{roleID}, []string{"*"}); err != nil {
		t.Fatal(err)
	}
	perms, _ = s.EffectivePermissions(ctx, "alice")
	if !perms[comp1][PermDeploy] || !perms[comp2][PermDeploy] {
		t.Fatalf("角色权限应作用于组内全部组件: %v", perms)
	}

	// 非法角色 ID 应报错
	if err := s.SetGroupPermission(ctx, teamID, groupID, nil, []int64{999999}, []string{"*"}); err == nil {
		t.Fatal("无效角色 ID 应报错")
	}
	// 组外服务名应被过滤（不报错但不生效）
	if err := s.SetGroupPermission(ctx, teamID, groupID, []string{PermRead}, nil, []string{"not-in-group"}); err != nil {
		t.Fatal(err)
	}
	perms, _ = s.EffectivePermissions(ctx, "alice")
	if perms[comp1][PermRead] {
		t.Fatal("组外服务名的授权不应生效")
	}
}
