package task

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// Delete 删除任务及其批次、逐机状态和历史指令。
//
// 安全约束：
//   - 只允许 draft 或终态任务；running/paused 必须先取消，避免删除正在执行的升级；
//   - 即使任务已是终态，只要仍有 pending/dispatched/accepted/running 指令也拒绝删除；
//   - 租户隔离：非本租户任务一律返回“任务不存在”。
//
// task_batches / task_hosts / commands 均通过外键 ON DELETE CASCADE 清理。
func (s *Service) Delete(ctx context.Context, sc scope.Scope, taskID int64) error {
	if err := s.checkTaskOwner(ctx, sc, taskID); err != nil {
		return err
	}
	tCond, tArgs := sc.Where("", 2)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	deletable := func(status string) bool {
		switch status {
		case "draft", "success", "failed", "canceled", "rolled_back":
			return true
		default:
			return false
		}
	}

	// 先做一次无锁检查：跨租户任务直接视为不存在，避免锁住他人的数据。
	var status string
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM upgrade_tasks WHERE id=$1 AND `+tCond,
		append([]any{taskID}, tArgs...)...).Scan(&status)
	if err == sql.ErrNoRows {
		return fmt.Errorf("任务不存在")
	}
	if err != nil {
		return err
	}
	if !deletable(status) {
		return fmt.Errorf("任务状态为 %s，请先取消任务再删除", status)
	}

	// 锁顺序与 Cancel / completeBatch / dispatchHost 一致：
	// task_batches -> task_hosts -> commands -> upgrade_tasks。
	// 子表先锁可避免与编排器互相等待，并阻止删除过程中插入新指令。
	lockRows := func(query string) error {
		rows, err := tx.QueryContext(ctx, query, taskID)
		if err != nil {
			return err
		}
		rows.Close()
		return rows.Err()
	}
	for _, query := range []string{
		`SELECT 1 FROM task_batches WHERE task_id=$1 FOR UPDATE`,
		`SELECT 1 FROM task_hosts WHERE task_id=$1 FOR UPDATE`,
		`SELECT 1 FROM commands WHERE task_id=$1 FOR UPDATE`,
	} {
		if err := lockRows(query); err != nil {
			return err
		}
	}

	// 最后锁任务并复查状态，防止检查后被启动/暂停。
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM upgrade_tasks WHERE id=$1 AND `+tCond+` FOR UPDATE`,
		append([]any{taskID}, tArgs...)...).Scan(&status)
	if err == sql.ErrNoRows {
		return fmt.Errorf("任务不存在")
	}
	if err != nil {
		return err
	}
	if !deletable(status) {
		return fmt.Errorf("任务状态为 %s，请先取消任务再删除", status)
	}

	var active int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commands
		WHERE task_id=$1 AND status IN ('pending','dispatched','accepted','running')`,
		taskID).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return fmt.Errorf("任务仍有 %d 条未完成指令，请先取消任务", active)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM upgrade_tasks WHERE id=$1 AND `+tCond,
		append([]any{taskID}, tArgs...)...); err != nil {
		return err
	}
	return tx.Commit()
}
