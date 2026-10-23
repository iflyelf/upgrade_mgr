package org

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/flyiam"
)

// TestReplaceAndListDepartments 组织树整体替换（删除的部门不残留）与排序。
func TestReplaceAndListDepartments(t *testing.T) {
	db := setupDB(t)
	s := NewService(db)
	ctx := context.Background()
	sfx := fmt.Sprintf("%d", time.Now().UnixNano())
	root := "g-" + sfx
	child := "rd-" + sfx

	t.Cleanup(func() {
		db.Exec(`DELETE FROM org_departments WHERE dept_id LIKE $1`, "%-"+sfx)
	})

	n, err := s.ReplaceDepartments(ctx, []flyiam.Department{
		{DeptID: root, DeptName: "集团", Level: 0, FullPath: "集团"},
		{DeptID: child, DeptName: "研发", ParentID: root, Level: 1,
			FullPath: "集团/研发", UserCount: 10},
		{DeptID: "  ", DeptName: "空", Level: 0}, // 空 ID 应跳过
	})
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("应写入 2 个部门, got %d", n)
	}

	list, err := s.ListDepartments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("应有 2 个部门, got %d: %+v", len(list), list)
	}
	if list[0].DeptID != root || list[1].DeptID != child {
		t.Fatalf("排序不符: %+v", list)
	}
	if list[1].ParentID != root || list[1].UserCount != 10 || list[1].SyncedAt.IsZero() {
		t.Fatalf("子部门字段不符: %+v", list[1])
	}

	// 第二次同步只保留一个节点：旧节点必须被清除
	n, err = s.ReplaceDepartments(ctx, []flyiam.Department{
		{DeptID: root, DeptName: "集团（新）", Level: 0, FullPath: "集团（新）"},
	})
	if err != nil || n != 1 {
		t.Fatalf("第二次同步失败: n=%d err=%v", n, err)
	}
	list, _ = s.ListDepartments(ctx)
	if len(list) != 1 || list[0].DeptName != "集团（新）" {
		t.Fatalf("替换后应只剩新节点: %+v", list)
	}

	if cnt, err := s.DepartmentCount(ctx); err != nil || cnt != 1 {
		t.Fatalf("DepartmentCount=%d err=%v", cnt, err)
	}
}
