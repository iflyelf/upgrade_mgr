package agentgw

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// FinalizeCompletedStages 修复「阶段已经走到完成，但最终 report 丢失」的卡单。
//
// 旧 Agent 的 report 只发送一次且忽略错误；如果日志包含 NUL/非法 UTF-8 或
// 网络抖动，指令会永久停在 running。这里根据阶段日志做保守推断：
//   - 最后阶段是 done，且日志包含执行安装/启动服务/安装后处理/验证版本
//     等“成功路径”标志 → 补记成功；
//   - 最后阶段是 failed → 补记失败；
//   - done 但无法确认成功且已超过 10 分钟 → 补记 timeout（不谎报成功）。
//
// 新 Agent 会在 done/failed 阶段直接由 UpdateStage 落终态，最终 report 还会
// 重试，因此这里主要覆盖旧 Agent 的历史卡单。
func (g *Gateway) FinalizeCompletedStages(ctx context.Context) (int, error) {
	now := time.Now().Format("2006-01-02 15:04:05")
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	total := 0

	successLog := fmt.Sprintf("[%s] 阶段已完成且包含成功步骤，服务端补记为成功\n", now)
	res, err := tx.ExecContext(ctx, `
		UPDATE commands
		SET status='success', finished_at=NOW(), last_stage='done', last_stage_at=NOW(),
		    result=jsonb_build_object('status','success','stage','done',
		        'to_version', COALESCE(payload->>'to_version',''), 'auto_finalized', true),
		    log=COALESCE(log,'')||$1
		WHERE status IN ('dispatched','accepted','running')
		  AND last_stage='done'
		  AND COALESCE(last_stage_at, started_at, dispatched_at, created_at) < NOW() - INTERVAL '2 minutes'
		  AND (log LIKE '%阶段: 执行安装%'
		       OR log LIKE '%阶段: 启动服务%'
		       OR log LIKE '%阶段: 安装后处理%'
		       OR log LIKE '%阶段: 验证版本%'
		       OR log LIKE '%升级成功%')`, successLog)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		total += int(n)
	}

	failLog := fmt.Sprintf("[%s] 服务端根据失败阶段补记失败（未收到最终结果）\n", now)
	res, err = tx.ExecContext(ctx, `
		UPDATE commands
		SET status='failed', finished_at=NOW(), last_stage='failed', last_stage_at=NOW(),
		    result=jsonb_build_object('status','failed','stage','failed','auto_finalized',true),
		    log=COALESCE(log,'')||$1
		WHERE status IN ('dispatched','accepted','running')
		  AND last_stage='failed'
		  AND COALESCE(last_stage_at, started_at, dispatched_at, created_at) < NOW() - INTERVAL '2 minutes'`,
		failLog)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		total += int(n)
	}

	// done 但没有足够证据判断成功：等待 10 分钟后按 timeout 处理，避免永久运行。
	timeoutLog := fmt.Sprintf("[%s] 阶段已完成但未收到最终结果，无法确认成功，按超时处理\n", now)
	res, err = tx.ExecContext(ctx, `
		UPDATE commands
		SET status='timeout', finished_at=NOW(), last_stage_at=NOW(),
		    result=jsonb_build_object('status','timeout','stage','done','auto_finalized',true),
		    log=COALESCE(log,'')||$1
		WHERE status IN ('dispatched','accepted','running')
		  AND last_stage='done'
		  AND COALESCE(last_stage_at, started_at, dispatched_at, created_at) < NOW() - INTERVAL '10 minutes'`,
		timeoutLog)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		total += int(n)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}

// ReapExpired 回收长时间没有阶段/结果上报的指令。
//
// 典型场景：
//   - Agent 收到指令后进程崩溃/机器重启，report 永远不会到达；
//   - self_update 替换失败但 Agent 也未能上报；
//   - 旧 Agent 的结果上报因日志包含 NUL 等原因被服务端拒绝。
//
// 没有这个兜底时，commands/task_hosts/upgrade_tasks 会永久停在 running。
// 每次 phase 上报都会续租（见 UpdateStage），因此正常执行中的长任务不会被误杀。
func (g *Gateway) ReapExpired(ctx context.Context) (int, error) {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 一次最多处理 500 条，避免大量离线 Agent 时单个事务过大。
	// 这里先不加行锁：真正的状态更新会带 lease/status 条件重新校验。
	// 逐条按「先 task_hosts、后 commands」加锁，与 Report/UpdateStage/Retry
	// 的顺序一致，避免与 Agent 上报并发时互相等待。
	rows, err := tx.QueryContext(ctx, `
		SELECT id, agent_id, COALESCE(task_host_id,0) FROM commands
		WHERE status IN ('dispatched','accepted','running')
		  AND lease_expire_at IS NOT NULL
		  AND lease_expire_at < NOW()
		ORDER BY id
		LIMIT 500`)
	if err != nil {
		return 0, err
	}
	type expired struct {
		id     int64
		agent  string
		taskHi int64
	}
	var list []expired
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.id, &e.agent, &e.taskHi); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	line := fmt.Sprintf("[%s] 超过租约未收到 Agent 阶段/结果上报，服务端标记超时\n",
		time.Now().Format("2006-01-02 15:04:05"))
	agents := map[string]bool{}
	reaped := 0
	for _, e := range list {
		// 与 Retry/Cancel/Report 保持相同的加锁顺序：先 task_hosts，再 commands。
		// 锁住逐机记录后重新校验指令状态，避免与“重新执行”并发时，
		// 把刚取消的旧指令/刚重置的 pending 又标成 timeout。
		if e.taskHi > 0 {
			var lockedID int64
			err := tx.QueryRowContext(ctx,
				`SELECT id FROM task_hosts WHERE id=$1 FOR UPDATE`, e.taskHi).Scan(&lockedID)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return 0, err
			}
		}
		var curStatus string
		var curLease sql.NullTime
		var curExpired bool
		err := tx.QueryRowContext(ctx, `
			SELECT status, lease_expire_at, COALESCE(lease_expire_at < NOW(), false)
			FROM commands
			WHERE id=$1 FOR UPDATE`, e.id).Scan(&curStatus, &curLease, &curExpired)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return 0, err
		}
		if curStatus != protocol.CmdDispatched && curStatus != protocol.CmdAccepted &&
			curStatus != protocol.CmdRunning {
			continue
		}
		// 用数据库时间判断租约是否过期，避免服务端与数据库时钟偏差导致
		// “数据库选中了过期指令，应用侧却认为租约仍有效”而反复空转。
		if !curLease.Valid || !curExpired {
			continue
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE commands
			SET status=$2::varchar, last_stage=$3::varchar, last_stage_at=NOW(), finished_at=NOW(),
			    result=COALESCE(result, jsonb_build_object('status',$2::varchar,'stage',$3::varchar)),
			    log=COALESCE(log,'')||$4
			WHERE id=$1 AND status IN ('dispatched','accepted','running')`,
			e.id, protocol.CmdTimeout, protocol.StageFailed, line); err != nil {
			return 0, err
		}
		if e.taskHi > 0 {
			if _, err := tx.ExecContext(ctx, `
				UPDATE task_hosts
				SET status=$2::varchar, stage=$3::varchar, finished_at=NOW(), log=COALESCE(log,'')||$4
				WHERE id=$1 AND status IN ('pending','running')`,
				e.taskHi, protocol.CmdTimeout, protocol.StageFailed, line); err != nil {
				return 0, err
			}
		}
		agents[e.agent] = true
		reaped++
	}

	// 指令已终态且该 Agent 没有其他活动指令时，立即退出维护窗口。
	// 否则主机列表会一直显示 upgrading，直到 maintenance_until 自然过期。
	for agentID := range agents {
		if _, err := tx.ExecContext(ctx, `
			UPDATE hosts
			SET maintenance_until=NULL, maintenance_reason='',
			    agent_status=CASE WHEN agent_status='upgrading' THEN 'online' ELSE agent_status END
			WHERE agent_id=$1
			  AND NOT EXISTS (
			      SELECT 1 FROM commands c
			      WHERE c.agent_id=$1
			        AND c.status IN ('pending','dispatched','accepted','running')
			  )`, agentID); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return reaped, nil
}

// SyncTaskHostsFromCommands 把已终态指令同步回 task_hosts。
//
// 防御历史数据/并发导致的「指令 success 但逐机记录仍 running」。
// 只取每个 task_host 最新的一条终态指令，避免历史指令覆盖当前状态。
func (g *Gateway) SyncTaskHostsFromCommands(ctx context.Context) (int, error) {
	res, err := g.db.ExecContext(ctx, `
		UPDATE task_hosts th
		SET status=CASE c.status WHEN 'expired' THEN 'timeout' ELSE c.status END,
		    stage=COALESCE(NULLIF(c.last_stage,''), th.stage),
		    to_version=COALESCE(NULLIF(c.result->>'to_version',''), NULLIF(c.payload->>'to_version',''), th.to_version),
		    exit_code=CASE WHEN COALESCE(c.result->>'exit_code','') ~ '^-?[0-9]+$'
		                   THEN (c.result->>'exit_code')::int ELSE th.exit_code END,
		    log=CASE WHEN COALESCE(c.log,'') <> '' THEN c.log ELSE th.log END,
		    finished_at=COALESCE(c.finished_at, NOW())
		FROM (
		    SELECT DISTINCT ON (c.task_host_id)
		           c.task_host_id, c.status, c.last_stage, c.result, c.payload, c.log,
		           c.finished_at, c.created_at
		    FROM commands c
		    JOIN task_hosts active_th ON active_th.id = c.task_host_id
		    WHERE c.task_host_id IS NOT NULL
		      AND c.status IN ('success','failed','rolled_back','timeout','expired')
		      AND active_th.status IN ('pending','running')
		    ORDER BY c.task_host_id, c.id DESC
		) c
		WHERE c.task_host_id = th.id
		  AND th.status IN ('pending','running')
		  -- 只同步「本次执行」的终态指令。retry 会把 started_at 设为重试时间，
		  -- 早于它的历史 failed/timeout 不能立刻把 pending 又改回失败。
		  AND (th.started_at IS NULL OR COALESCE(c.finished_at, c.created_at) >= th.started_at)
		  AND NOT EXISTS (
		      SELECT 1 FROM commands c2
		      WHERE c2.task_host_id=th.id
		        AND c2.status IN ('pending','dispatched','accepted','running')
		  )`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ReconcileTasks 是编排器每轮推进前的轻量对账：
//  1. 回收超时指令；
//  2. 把已终态指令同步到 task_hosts；
//  3. 清理没有活动指令的主机维护窗口。
//
// 即使 Agent 的 report 永久丢失，任务也会在租约到期后进入 timeout，
// 批次得以继续推进/结束，而不是永久 running。
func (g *Gateway) ReconcileTasks(ctx context.Context) (int, error) {
	finalized, err := g.FinalizeCompletedStages(ctx)
	if err != nil {
		return finalized, err
	}
	reaped, err := g.ReapExpired(ctx)
	if err != nil {
		return finalized + reaped, err
	}
	synced, err := g.SyncTaskHostsFromCommands(ctx)
	if err != nil {
		return finalized + reaped, err
	}
	// 清理僵尸维护窗口：没有活动指令时，upgrading 不应继续显示。
	if _, err := g.db.ExecContext(ctx, `
		UPDATE hosts h
		SET maintenance_until=NULL, maintenance_reason='',
		    agent_status=CASE WHEN agent_status='upgrading' THEN 'online' ELSE agent_status END
		WHERE maintenance_reason='upgrading'
		  AND NOT EXISTS (
		      SELECT 1 FROM commands c
		      WHERE c.agent_id=h.agent_id
		        AND c.status IN ('pending','dispatched','accepted','running')
		  )`); err != nil {
		return finalized + reaped + synced, err
	}
	return finalized + reaped + synced, nil
}
