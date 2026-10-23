package org

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestTeamOwnerPolicy 团队级任务操作策略的归一化与聚合优先级。
func TestTeamOwnerPolicy(t *testing.T) {
	db := setupDB(t)
	s := NewService(db)
	ctx := context.Background()
	sfx := time.Now().UnixNano()
	user := fmt.Sprintf("owner-u-%d", sfx)

	// 未加入任何团队 → 返回空串（调用方回退全局设置）
	if p, err := s.TeamOwnerPolicy(ctx, user); err != nil || p != "" {
		t.Fatalf("未加入团队应返回空串, got %q err=%v", p, err)
	}

	// 加入一个 on 团队
	teamOn, err := s.CreateTeamWithPolicy(ctx, fmt.Sprintf("t-on-%d", sfx), "", "仅本人",
		[]string{user}, "tester", "on")
	if err != nil {
		t.Fatalf("创建 on 团队失败: %v", err)
	}
	t.Cleanup(func() { s.DeleteTeam(ctx, teamOn) })

	if p, err := s.TeamOwnerPolicy(ctx, user); err != nil || p != "on" {
		t.Fatalf("应返回 on, got %q err=%v", p, err)
	}

	// 再加入一个 off 团队：on 优先（更严格），避免绕过
	teamOff, err := s.CreateTeamWithPolicy(ctx, fmt.Sprintf("t-off-%d", sfx), "", "团队共享",
		[]string{user}, "tester", "off")
	if err != nil {
		t.Fatalf("创建 off 团队失败: %v", err)
	}
	t.Cleanup(func() { s.DeleteTeam(ctx, teamOff) })
	if p, err := s.TeamOwnerPolicy(ctx, user); err != nil || p != "on" {
		t.Fatalf("on 应优先于 off, got %q err=%v", p, err)
	}

	// 删除 on 团队后只剩 off → off
	if err := s.DeleteTeam(ctx, teamOn); err != nil {
		t.Fatalf("删除 on 团队失败: %v", err)
	}
	if p, err := s.TeamOwnerPolicy(ctx, user); err != nil || p != "off" {
		t.Fatalf("应返回 off, got %q err=%v", p, err)
	}

	// 加入 inherit 团队后（off + inherit）→ inherit（回退全局）
	teamInherit, err := s.CreateTeamWithPolicy(ctx, fmt.Sprintf("t-inh-%d", sfx), "", "跟随",
		[]string{user}, "tester", "inherit")
	if err != nil {
		t.Fatalf("创建 inherit 团队失败: %v", err)
	}
	t.Cleanup(func() { s.DeleteTeam(ctx, teamInherit) })
	if p, err := s.TeamOwnerPolicy(ctx, user); err != nil || p != "inherit" {
		t.Fatalf("off+inherit 应回退 inherit, got %q err=%v", p, err)
	}

	// 无关用户不受影响
	if p, err := s.TeamOwnerPolicy(ctx, fmt.Sprintf("nobody-%d", sfx)); err != nil || p != "" {
		t.Fatalf("无关用户应返回空串, got %q err=%v", p, err)
	}
}

// TestNormalizeTeamOwnerOnly 策略值容错（空/未知 → inherit）。
func TestNormalizeTeamOwnerOnly(t *testing.T) {
	cases := map[string]string{
		"on": "on", "ON": "on", "true": "on", "1": "on",
		"off": "off", "False": "off", "0": "off",
		"": "inherit", "inherit": "inherit", "whatever": "inherit",
	}
	for in, want := range cases {
		if got := NormalizeTeamOwnerOnly(in); got != want {
			t.Fatalf("NormalizeTeamOwnerOnly(%q)=%q, want %q", in, got, want)
		}
	}
}

// TestUpdateTeamOwnerPolicy 更新团队策略（空串表示不修改）。
func TestUpdateTeamOwnerPolicy(t *testing.T) {
	db := setupDB(t)
	s := NewService(db)
	ctx := context.Background()
	sfx := time.Now().UnixNano()
	name := fmt.Sprintf("team-upd-%d", sfx)

	id, err := s.CreateTeam(ctx, name, "", "desc", nil, "tester")
	if err != nil {
		t.Fatalf("创建团队失败: %v", err)
	}
	t.Cleanup(func() { s.DeleteTeam(ctx, id) })

	teams, err := s.ListTeams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tm := range teams {
		if tm.ID == id {
			found = true
			if tm.OwnerOnly != "inherit" {
				t.Fatalf("默认策略应为 inherit, got %q", tm.OwnerOnly)
			}
		}
	}
	if !found {
		t.Fatal("新建团队未出现在列表")
	}

	// 空串：不修改
	if err := s.UpdateTeamWithPolicy(ctx, id, name, "desc2", 1, ""); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	teams, _ = s.ListTeams(ctx)
	for _, tm := range teams {
		if tm.ID == id && tm.OwnerOnly != "inherit" {
			t.Fatalf("空串不应修改策略, got %q", tm.OwnerOnly)
		}
	}

	// 显式 on
	if err := s.UpdateTeamWithPolicy(ctx, id, name, "desc2", 1, "on"); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	teams, _ = s.ListTeams(ctx)
	for _, tm := range teams {
		if tm.ID == id && tm.OwnerOnly != "on" {
			t.Fatalf("策略应更新为 on, got %q", tm.OwnerOnly)
		}
	}
}

// TestCanShareTasks 同一 off 团队成员可互相操作任务；on 团队不算。
func TestCanShareTasks(t *testing.T) {
	db := setupDB(t)
	s := NewService(db)
	ctx := context.Background()
	sfx := time.Now().UnixNano()
	alice := fmt.Sprintf("alice-%d", sfx)
	bob := fmt.Sprintf("bob-%d", sfx)
	carol := fmt.Sprintf("carol-%d", sfx)

	offID, err := s.CreateTeamWithPolicy(ctx, fmt.Sprintf("share-off-%d", sfx), "", "",
		[]string{alice, bob}, "tester", "off")
	if err != nil {
		t.Fatalf("创建 off 团队失败: %v", err)
	}
	t.Cleanup(func() { s.DeleteTeam(ctx, offID) })

	ok, err := s.CanShareTasks(ctx, bob, alice)
	if err != nil || !ok {
		t.Fatalf("同 off 团队应允许: ok=%v err=%v", ok, err)
	}
	ok, err = s.CanShareTasks(ctx, carol, alice)
	if err != nil || ok {
		t.Fatalf("非团队成员不应允许: ok=%v err=%v", ok, err)
	}
	ok, _ = s.CanShareTasks(ctx, alice, alice)
	if !ok {
		t.Fatal("本人应允许")
	}

	// on 团队不产生共享
	onID, err := s.CreateTeamWithPolicy(ctx, fmt.Sprintf("share-on-%d", sfx), "", "",
		[]string{alice, carol}, "tester", "on")
	if err != nil {
		t.Fatalf("创建 on 团队失败: %v", err)
	}
	t.Cleanup(func() { s.DeleteTeam(ctx, onID) })
	ok, err = s.CanShareTasks(ctx, carol, alice)
	if err != nil || ok {
		t.Fatalf("on 团队不应共享: ok=%v err=%v", ok, err)
	}
}
