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
	tCond, tArgs := sc.Where("", 2)
	var status string
	var componentID int64
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

	// 校验组件存在
	if _, err := s.compRepo.Get(ctx, sc, componentID); err != nil {
		return err
	}
	// 置为 running，实际的批次下发由 Orchestrator 按灰度策略逐批推进。
	_, err = s.db.ExecContext(ctx, `UPDATE upgrade_tasks SET status='running', updated_at=NOW() WHERE id=$1 AND `+tCond,
		append([]any{taskID}, tArgs...)...)
	return err
}

// Promote 人工推进：auto_promote=false 时，将 paused 任务恢复为 running 以继续下一批。
// 仅限 paused 状态（失败超阈值暂停后，Promote 跳过 failed 批次继续推进）。
func (s *Service) Promote(ctx context.Context, sc scope.Scope, taskID int64) error {
	tCond, tArgs := sc.Where("", 2)
	res, err := s.db.ExecContext(ctx,
		`UPDATE upgrade_tasks SET status='running', updated_at=NOW() WHERE id=$1 AND `+tCond+` AND status='paused'`,
		append([]any{taskID}, tArgs...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("任务非暂停状态，无法推进")
	}
	return nil
}

// Pause 暂停任务（仅限 running 状态）。
func (s *Service) Pause(ctx context.Context, sc scope.Scope, taskID int64) error {
	tCond, tArgs := sc.Where("", 2)
	_, err := s.db.ExecContext(ctx,
		`UPDATE upgrade_tasks SET status='paused', updated_at=NOW() WHERE id=$1 AND `+tCond+` AND status='running'`,
		append([]any{taskID}, tArgs...)...)
	return err
}

// Cancel 取消任务：仅作用于非终态任务（draft/running/paused），
// 将未完成的 commands 和 task_hosts 置为 canceled。
func (s *Service) Cancel(ctx context.Context, sc scope.Scope, taskID int64) error {
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

	// 2. 取消未完成的 commands（pending/dispatched/accepted/running）
	_, err = tx.ExecContext(ctx, `
		UPDATE commands 
		SET status='canceled', finished_at=NOW() 
		WHERE task_id=$1 AND status IN ('pending','dispatched','accepted','running')`, taskID)
	if err != nil {
		return err
	}

	// 3. 取消未完成的 task_hosts（pending/running）
	_, err = tx.ExecContext(ctx, `
		UPDATE task_hosts 
		SET status='canceled', finished_at=NOW() 
		WHERE task_id=$1 AND status IN ('pending','running')`, taskID)
	if err != nil {
		return err
	}

	// 4. 更新任务状态为 canceled
	_, err = tx.ExecContext(ctx,
		`UPDATE upgrade_tasks SET status='canceled', updated_at=NOW() WHERE id=$1 AND `+tCond,
		append([]any{taskID}, tArgs...)...)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func toComponentSpec(c *component.Component) protocol.ComponentSpec {
	return protocol.ComponentSpec{
		Name: c.Name, DeployMode: c.DeployMode, InstallPath: c.InstallPath, BackupDir: c.BackupDir,
		ExecPath: c.ExecPath, ExecArgs: c.ExecArgs, ExecEnv: c.ExecEnv, WorkDir: c.WorkDir,
		ExecTimeout: c.ExecTimeout, IgnoreError: c.IgnoreError, PreInstall: c.PreInstall,
		PostInstall: c.PostInstall, ServiceName: c.ServiceName, ServiceMgr: c.ServiceMgr,
		StopCmd: c.StopCmd, StartCmd: c.StartCmd, RestartCmd: c.RestartCmd,
		VersionCmd: c.VersionCmd, VersionRegex: c.VersionRegex, HealthCmd: c.HealthCmd,
		ConsulMaint: c.ConsulMaint,
	}
}
