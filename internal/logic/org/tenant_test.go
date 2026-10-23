package org

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/flyiam"
)

// TestTenantIsolation 角色/团队/服务组/组织架构按租户隔离；
// superadmin 可跨租户查看；不同租户允许同名。
func TestTenantIsolation(t *testing.T) {
	db := setupDB(t)
	s := NewService(db)
	base := context.Background()
	sfx := time.Now().UnixNano()

	t1 := auth.WithUser(base, &auth.User{Username: "org-admin1", TenantID: 1, IsAdmin: true})
	t2 := auth.WithUser(base, &auth.User{Username: "org-admin2", TenantID: 2, IsAdmin: true})
	su := auth.WithUser(base, &auth.User{Username: "org-root", TenantID: 1,
		Roles: []string{auth.RoleSuperAdmin}})

	containsTeam := func(list []Team, id int64) bool {
		for _, x := range list {
			if x.ID == id {
				return true
			}
		}
		return false
	}
	teamName := fmt.Sprintf("tenant-team-%d", sfx)
	team1, err := s.CreateTeam(t1, teamName, "", "", nil, "admin1")
	if err != nil {
		t.Fatal(err)
	}
	team2, err := s.CreateTeam(t2, teamName, "", "", nil, "admin2") // 同名，不同租户
	if err != nil {
		t.Fatalf("不同租户应允许同名团队: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM teams WHERE id IN ($1,$2)`, team1, team2)
		db.Exec(`DELETE FROM roles WHERE name LIKE $1`, "tenant-role-%"+fmt.Sprint(sfx)+"%")
		db.Exec(`DELETE FROM service_groups WHERE name LIKE $1`, "tenant-group-%"+fmt.Sprint(sfx)+"%")
		db.Exec(`DELETE FROM org_departments WHERE dept_id LIKE $1`, "tenant-dept-"+fmt.Sprint(sfx)+"%")
	})

	if l1 := mustTeams(t, s, t1); !containsTeam(l1, team1) || containsTeam(l1, team2) {
		t.Fatalf("租户 1 团队列表不符: %+v", l1)
	}
	if l2 := mustTeams(t, s, t2); !containsTeam(l2, team2) || containsTeam(l2, team1) {
		t.Fatalf("租户 2 团队列表不符: %+v", l2)
	}
	if lsu := mustTeams(t, s, su); !containsTeam(lsu, team1) || !containsTeam(lsu, team2) {
		t.Fatalf("superadmin 应看到全部团队: %+v", lsu)
	}
	if err := s.UpdateTeam(t2, team1, teamName, "", 1); err == nil {
		t.Fatal("跨租户更新团队应失败")
	}
	if err := s.DeleteTeam(t2, team1); err == nil {
		t.Fatal("跨租户删除团队应失败")
	}

	// 服务组
	groupName := fmt.Sprintf("tenant-group-%d", sfx)
	group1, err := s.CreateGroup(t1, groupName, "", "", nil, "admin1")
	if err != nil {
		t.Fatal(err)
	}
	group2, err := s.CreateGroup(t2, groupName, "", "", nil, "admin2")
	if err != nil {
		t.Fatalf("不同租户应允许同名服务组: %v", err)
	}
	if l1 := mustGroups(t, s, t1); !containsGroup(l1, group1) || containsGroup(l1, group2) {
		t.Fatalf("租户 1 服务组列表不符")
	}
	if err := s.DeleteGroup(t2, group1); err == nil {
		t.Fatal("跨租户删除服务组应失败")
	}

	// 角色
	roleName := fmt.Sprintf("tenant-role-%d", sfx)
	role1, err := s.CreateRole(t1, roleName, "", "", []string{PermRead})
	if err != nil {
		t.Fatal(err)
	}
	role2, err := s.CreateRole(t2, roleName, "", "", []string{PermRead})
	if err != nil {
		t.Fatalf("不同租户应允许同名角色: %v", err)
	}
	if l1 := mustRoles(t, s, t1); !containsRole(l1, role1) || containsRole(l1, role2) {
		t.Fatalf("租户 1 角色列表不符")
	}
	if err := s.DeleteRole(t2, role1); err == nil {
		t.Fatal("跨租户删除角色应失败")
	}

	// 组织架构：替换只影响本租户
	dept1 := fmt.Sprintf("tenant-dept-%d-a", sfx)
	dept2 := fmt.Sprintf("tenant-dept-%d-b", sfx)
	if _, err := s.ReplaceDepartments(t1, []flyiam.Department{
		{DeptID: dept1, DeptName: "租户1部门", Level: 0, FullPath: "租户1部门"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceDepartments(t2, []flyiam.Department{
		{DeptID: dept2, DeptName: "租户2部门", Level: 0, FullPath: "租户2部门"},
	}); err != nil {
		t.Fatal(err)
	}
	d1, err := s.ListDepartments(t1)
	if err != nil {
		t.Fatal(err)
	}
	if len(d1) != 1 || d1[0].DeptID != dept1 {
		t.Fatalf("租户 1 组织架构不符: %+v", d1)
	}
	dsu, _ := s.ListDepartments(su)
	if len(dsu) < 2 {
		t.Fatalf("superadmin 应看到全部组织: %+v", dsu)
	}
}

func mustTeams(t *testing.T, s *Service, ctx context.Context) []Team {
	t.Helper()
	v, err := s.ListTeams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func containsTeam(list []Team, id int64) bool {
	for _, x := range list {
		if x.ID == id {
			return true
		}
	}
	return false
}
func mustGroups(t *testing.T, s *Service, ctx context.Context) []Group {
	t.Helper()
	v, err := s.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func containsGroup(list []Group, id int64) bool {
	for _, x := range list {
		if x.ID == id {
			return true
		}
	}
	return false
}
func mustRoles(t *testing.T, s *Service, ctx context.Context) []Role {
	t.Helper()
	v, err := s.ListRoles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func containsRole(list []Role, id int64) bool {
	for _, x := range list {
		if x.ID == id {
			return true
		}
	}
	return false
}
