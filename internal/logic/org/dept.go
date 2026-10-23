package org

import (
	"context"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/flyiam"
)

// Department 本地组织节点（FlyIAM 组织树快照）。
type Department struct {
	DeptID    string    `json:"dept_id"`
	DeptName  string    `json:"dept_name"`
	ParentID  string    `json:"parent_id"`
	Level     int       `json:"level"`
	FullPath  string    `json:"full_path"`
	UserCount int       `json:"user_count"`
	SyncedAt  time.Time `json:"synced_at"`
}

// ReplaceDepartments 用上游快照整体替换本地组织树。
//
// 事务内先清空再写入：上游已删除的部门不会残留；读取方在事务提交前
// 看到的仍是旧快照，不会出现「空树」中间态。
func (s *Service) ReplaceDepartments(ctx context.Context, deps []flyiam.Department) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM org_departments`); err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	n := 0
	for _, d := range deps {
		deptID := cutRunes(strings.TrimSpace(d.DeptID), 200)
		if deptID == "" || seen[deptID] {
			continue
		}
		seen[deptID] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO org_departments
			    (dept_id, dept_name, parent_id, level, full_path, user_count, synced_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,NOW(),NOW())
			ON CONFLICT (dept_id) DO UPDATE SET
			    dept_name=EXCLUDED.dept_name, parent_id=EXCLUDED.parent_id,
			    level=EXCLUDED.level, full_path=EXCLUDED.full_path,
			    user_count=EXCLUDED.user_count, synced_at=NOW(), updated_at=NOW()`,
			deptID,
			cutRunes(strings.TrimSpace(d.DeptName), 300),
			cutRunes(strings.TrimSpace(d.ParentID), 200),
			d.Level,
			strings.TrimSpace(d.FullPath),
			d.UserCount,
		); err != nil {
			return 0, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// ListDepartments 返回组织树快照，按完整路径排序（同名前缀自然相邻）。
func (s *Service) ListDepartments(ctx context.Context) ([]Department, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT dept_id, dept_name, parent_id, level, full_path, user_count, synced_at
		  FROM org_departments
		 ORDER BY full_path, dept_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Department, 0, 64)
	for rows.Next() {
		var d Department
		if err := rows.Scan(&d.DeptID, &d.DeptName, &d.ParentID, &d.Level,
			&d.FullPath, &d.UserCount, &d.SyncedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DepartmentCount 返回本地快照节点数（用于页面提示）。
func (s *Service) DepartmentCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_departments`).Scan(&n)
	return n, err
}

// cutRunes 按字符（而非字节）截断，避免把中文截成半个字符。
func cutRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
