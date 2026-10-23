package task

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// Start 启动任务：将 draft 置为 running，并为首批 pending 主机生成 commands。
//
// 简化的编排：P2 先将全部命中主机的 pending task_hosts 生成待下发指令（按批次顺序），
// Agent 侧串行执行；灰度门禁与自动推进在 P3 增强。
func (s *Service) Start(ctx context.Context, sc scope.Scope, taskID int64) error {
	if err := s.checkTaskOwner(ctx, sc, taskID); err != nil {
		return err
	}
	tCond, tArgs := sc.Where("", 2)
	var status string
	var componentID sql.NullInt64
	var version string
	err := s.db.QueryRowContext(ctx,
		`SELECT status, component_id, version FROM upgrade_tasks WHERE id=$1 AND `+tCond,
		append([]any{taskID}, tArgs...)...).
		Scan(&status, &componentID, &version)
	if err == sql.ErrNoRows {
		return fmt.Errorf("任务不存在")
	}
	if err != nil {
		return err
	}
	if status != "draft" && status != "paused" {
		return fmt.Errorf("任务状态 %s 不可启动", status)
	}

	// 校验组件存在（命令任务没有组件）
	if componentID.Valid {
		if _, err := s.compRepo.Get(ctx, sc, componentID.Int64); err != nil {
			return err
		}
	}
	// 置为 running，实际的批次下发由 Orchestrator 按灰度策略逐批推进。
	// 带状态条件并检查影响行数：防止检查后任务被取消/删除，或被并发操作改状态。
	res, err := s.db.ExecContext(ctx,
		`UPDATE upgrade_tasks SET status='running', updated_at=NOW()
		 WHERE id=$1 AND `+tCond+` AND status IN ('draft','paused')`,
		append([]any{taskID}, tArgs...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("任务状态已变化，请刷新后重试")
	}
	return nil
}

// Promote 人工推进：auto_promote=false 时，将 paused 任务恢复为 running 以继续下一批。
// 仅限 paused 状态（失败超阈值暂停后，Promote 跳过 failed 批次继续推进）。
//
// 「跳过失败批次」和「恢复任务」在同一事务内完成：先锁批次行，再更新任务，
// 与 completeBatch 的加锁顺序一致，避免并发 Promote/编排器互相死锁。
func (s *Service) Promote(ctx context.Context, sc scope.Scope, taskID int64) error {
	if err := s.checkTaskOwner(ctx, sc, taskID); err != nil {
		return err
	}
	tCond, tArgs := sc.Where("", 2)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 先锁失败批次，再恢复任务；与 completeBatch 的加锁顺序一致。
	if _, err := tx.ExecContext(ctx,
		`UPDATE task_batches SET status='skipped', finished_at=COALESCE(finished_at,NOW())
		 WHERE task_id=$1 AND status='failed'`, taskID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE upgrade_tasks SET status='running', updated_at=NOW() WHERE id=$1 AND `+tCond+` AND status='paused'`,
		append([]any{taskID}, tArgs...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("任务非暂停状态，无法推进")
	}
	return tx.Commit()
}

// Pause 暂停任务（仅限 running 状态）。
func (s *Service) Pause(ctx context.Context, sc scope.Scope, taskID int64) error {
	if err := s.checkTaskOwner(ctx, sc, taskID); err != nil {
		return err
	}
	tCond, tArgs := sc.Where("", 2)
	res, err := s.db.ExecContext(ctx,
		`UPDATE upgrade_tasks SET status='paused', updated_at=NOW() WHERE id=$1 AND `+tCond+` AND status='running'`,
		append([]any{taskID}, tArgs...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("任务非运行状态，无法暂停")
	}
	return nil
}

// Cancel 取消任务：仅作用于非终态任务（draft/running/paused），
// 将未完成的 commands 和 task_hosts 置为 canceled。
func (s *Service) Cancel(ctx context.Context, sc scope.Scope, taskID int64) error {
	if err := s.checkTaskOwner(ctx, sc, taskID); err != nil {
		return err
	}
	tCond, tArgs := sc.Where("", 2)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. 检查任务是否存在且为非终态（draft/running/paused）
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
	if status != "draft" && status != "running" && status != "paused" {
		return fmt.Errorf("任务已处于终态 %s，无法取消", status)
	}

	// 2. 锁顺序：task_batches -> task_hosts -> commands -> upgrade_tasks。
	//    与 completeBatch（batch -> task_hosts -> task）、dispatchHost/Report/
	//    UpdateStage（task_hosts -> commands）保持一致。先锁逐机记录可阻止
	//    dispatchHost 在取消过程中继续插入新指令，也避免事务互相等待。
	_, err = tx.ExecContext(ctx, `
		UPDATE task_batches
		SET status='canceled', finished_at=NOW()
		WHERE task_id=$1 AND status IN ('pending','running')`, taskID)
	if err != nil {
		return err
	}

	// 3. 取消逐机记录。
	_, err = tx.ExecContext(ctx, `
		UPDATE task_hosts
		SET status='canceled', finished_at=NOW()
		WHERE task_id=$1 AND status IN ('pending','running')`, taskID)
	if err != nil {
		return err
	}

	// 4. 取消未完成的 commands（pending/dispatched/accepted/running）
	_, err = tx.ExecContext(ctx, `
		UPDATE commands
		SET status='canceled', finished_at=NOW()
		WHERE task_id=$1 AND status IN ('pending','dispatched','accepted','running')`, taskID)
	if err != nil {
		return err
	}

	// 5. 更新任务状态为 canceled。带状态条件并检查影响行数：
	// 若任务在检查后已完成或已被删除，回滚本次取消，避免覆盖终态。
	res, err := tx.ExecContext(ctx,
		`UPDATE upgrade_tasks SET status='canceled', updated_at=NOW()
		 WHERE id=$1 AND `+tCond+` AND status IN ('draft','running','paused')`,
		append([]any{taskID}, tArgs...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("任务状态已变化，请刷新后重试")
	}

	return tx.Commit()
}

func toComponentSpec(c *component.Component) protocol.ComponentSpec {
	return protocol.ComponentSpec{
		Name: c.Name, DeployMode: c.DeployMode, InstallPath: c.InstallPath, BackupDir: c.BackupDir,
		ExecPath: c.ExecPath, ExecArgs: c.ExecArgs, ExecEnv: c.ExecEnv, WorkDir: c.WorkDir,
		ExecTimeout: c.ExecTimeout, ScriptURL: c.ScriptURL, IgnoreError: c.IgnoreError, PreInstall: c.PreInstall,
		PostInstall: c.PostInstall, PostInstallScript: c.PostInstallScript,
		ServiceName: c.ServiceName, ServiceMgr: c.ServiceMgr,
		StopCmd: c.StopCmd, StartCmd: c.StartCmd, RestartCmd: c.RestartCmd,
		VersionCmd: c.VersionCmd, VersionRegex: c.VersionRegex, HealthCmd: c.HealthCmd,
		ConsulMaint: c.ConsulMaint,
	}
}
