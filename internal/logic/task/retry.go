package task

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// RetryRequest 重新执行请求。
//
// TaskHostIDs 为空表示「重新执行该任务所有可重试主机」；
// 指定时只重置这些逐机记录（前端传 task_hosts.id，不是 hosts.id）。
type RetryRequest struct {
	TaskHostIDs []int64 `json:"task_host_ids"`
	// Force 表示即使主机仍处于执行中（Agent 租约未到期）也强制重置。
	// 服务端会取消旧指令；旧 Agent 进程若仍在执行，可能出现重复执行，
	// 因此前端必须二次确认。
	Force bool `json:"force"`
}

// RetrySkip 记录未重置的主机与原因。
type RetrySkip struct {
	TaskHostID int64  `json:"task_host_id"`
	HostID     int64  `json:"host_id,omitempty"`
	IP         string `json:"ip,omitempty"`
	Reason     string `json:"reason"`
}

// RetryResult 重新执行结果。
type RetryResult struct {
	Reset        int         `json:"reset"`
	SkippedCount int         `json:"skipped_count"`
	Skipped      []RetrySkip `json:"skipped"`
	TaskStatus   string      `json:"task_status"`
}

// activeCommandStatuses 未结束的指令状态。
func activeCommandStatuses() string {
	return "'pending','dispatched','accepted','running'"
}

// Retry 重新执行失败/卡住的主机。
//
// 只重置选中的 task_hosts，保留历史日志与旧指令（旧指令标记 canceled），
// 然后由编排器按原批次策略重新下发。已成功的主机默认不重跑，避免把正常
// 版本再升级/回滚一遍。
func (s *Service) Retry(ctx context.Context, sc scope.Scope, taskID int64, req RetryRequest) (RetryResult, error) {
	res := RetryResult{Skipped: []RetrySkip{}}
	if err := s.checkTaskOwner(ctx, sc, taskID); err != nil {
		return res, err
	}
	if err := s.checkTaskDeploy(ctx, sc, taskID); err != nil {
		return res, err
	}
	if len(req.TaskHostIDs) > 5000 {
		return res, fmt.Errorf("单次最多重新执行 5000 台主机")
	}

	// 先做无锁的租户/存在性检查：跨租户任务直接视为不存在，
	// 避免锁住他人的批次/逐机记录。
	tCond, tArgs := sc.Where("upgrade_tasks", 1)
	var taskStatus string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM upgrade_tasks WHERE id=$1 AND `+tCond,
		append([]any{taskID}, tArgs...)...).Scan(&taskStatus)
	if err == sql.ErrNoRows {
		return res, fmt.Errorf("任务不存在")
	}
	if err != nil {
		return res, err
	}
	if taskStatus == "draft" {
		return res, fmt.Errorf("任务尚未启动，请直接点击启动")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	// 锁顺序与 Cancel/Delete 保持一致：批次 -> 逐机 -> 指令 -> 任务，
	// 避免与编排器/取消操作互相等待。
	if _, err := tx.ExecContext(ctx,
		`SELECT 1 FROM task_batches WHERE task_id=$1 FOR UPDATE`, taskID); err != nil {
		return res, err
	}

	q := `
		SELECT th.id, th.host_id, h.ip, th.status, th.batch_index,
		       EXISTS(SELECT 1 FROM commands c
		              WHERE c.task_host_id=th.id AND c.status IN (` + activeCommandStatuses() + `)) AS has_active,
		       EXISTS(SELECT 1 FROM commands c
		              WHERE c.task_host_id=th.id AND c.status IN (` + activeCommandStatuses() + `)
		                AND c.lease_expire_at IS NOT NULL AND c.lease_expire_at > NOW()) AS has_live
		FROM task_hosts th
		JOIN hosts h ON h.id=th.host_id
		WHERE th.task_id=$1`
	args := []any{taskID}
	if len(req.TaskHostIDs) > 0 {
		q += ` AND th.id = ANY($2)`
		args = append(args, pq.Array(req.TaskHostIDs))
	}
	q += ` ORDER BY th.id FOR UPDATE OF th`

	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return res, err
	}
	type hostState struct {
		thID, hostID int64
		ip, status   string
		batch        int
		hasActive    bool
		hasLive      bool
	}
	var list []hostState
	for rows.Next() {
		var h hostState
		if err := rows.Scan(&h.thID, &h.hostID, &h.ip, &h.status, &h.batch,
			&h.hasActive, &h.hasLive); err != nil {
			rows.Close()
			return res, err
		}
		list = append(list, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	found := map[int64]bool{}
	var resetIDs []int64
	batchSet := map[int]bool{}
	for _, h := range list {
		found[h.thID] = true
		explicit := len(req.TaskHostIDs) > 0
		retryable := false
		reason := ""

		switch h.status {
		case "failed", "timeout", "canceled", "skipped", "rolled_back":
			// 回滚等价于本次升级失败：任务级重试应重新下发，否则前端按钮无效。
			retryable = true
		case "pending":
			// 任务级重试不重复处理已在队列中的主机；显式点击可以重置。
			retryable = explicit
			if !retryable {
				reason = "已在待下发队列"
			}
		case "running":
			if h.hasLive {
				// 租约有效说明 Agent 可能仍在下载/安装。任务级“重试失败/卡住”
				// 不能误伤正在执行的主机；只有显式对单台主机强制重试才允许。
				if explicit && req.Force {
					retryable = true
				} else {
					reason = "仍在执行中（Agent 租约未到期）；单台强制重试或等待超时"
				}
			} else {
				// 没有活动指令或租约已过期：典型卡单，直接重置。
				retryable = true
			}
		case "success":
			if explicit && req.Force {
				retryable = true
			} else {
				reason = "已成功，默认不重复执行"
			}
		default:
			reason = "状态 " + h.status + " 不支持重新执行"
		}

		if !retryable {
			res.SkippedCount++
			res.Skipped = append(res.Skipped, RetrySkip{
				TaskHostID: h.thID, HostID: h.hostID, IP: h.ip, Reason: reason,
			})
			continue
		}
		resetIDs = append(resetIDs, h.thID)
		batchSet[h.batch] = true
	}

	// 显式指定但不存在/不属于当前任务的主机也给出提示。
	for _, id := range req.TaskHostIDs {
		if !found[id] {
			res.SkippedCount++
			res.Skipped = append(res.Skipped, RetrySkip{
				TaskHostID: id, Reason: "主机不属于该任务或已被删除",
			})
		}
	}

	if len(resetIDs) == 0 {
		res.TaskStatus = taskStatus
		return res, tx.Commit()
	}

	line := fmt.Sprintf("[%s] 管理员触发重新执行：重置为待下发（历史日志保留）\n",
		time.Now().Format("2006-01-02 15:04:05"))

	// 先取消旧活动指令，释放唯一索引；旧 Agent 的迟到上报会被终态保护忽略。
	if _, err := tx.ExecContext(ctx, `
		UPDATE commands
		SET status='canceled', finished_at=NOW(), log=COALESCE(log,'')||$2
		WHERE task_host_id = ANY($1)
		  AND status IN (`+activeCommandStatuses()+`)`,
		pq.Array(resetIDs), line); err != nil {
		return res, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE task_hosts
		SET status='pending', stage='', exit_code=0,
		    -- 用 started_at 作为“本次重试基线”：SyncTaskHostsFromCommands 会忽略
		    -- 早于该时间的旧终态指令，避免重置后被旧的 failed/timeout 立刻覆盖。
		    started_at=NOW(), finished_at=NULL,
		    attempts=attempts+1,
		    log=COALESCE(log,'')||$2
		WHERE id = ANY($1)`, pq.Array(resetIDs), line); err != nil {
		return res, err
	}

	batches := make([]int, 0, len(batchSet))
	for b := range batchSet {
		batches = append(batches, b)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE task_batches
		SET status='pending', started_at=NULL, finished_at=NULL
		WHERE task_id=$1 AND batch_index = ANY($2) AND status <> 'pending'`,
		taskID, pq.Array(batches)); err != nil {
		return res, err
	}

	// 终态任务重新回到 running，让编排器继续推进。
	res.TaskStatus = "running"
	if _, err := tx.ExecContext(ctx, `
		UPDATE upgrade_tasks
		SET status='running', updated_at=NOW(),
		    updated_by=COALESCE(NULLIF($2,''), updated_by)
		WHERE id=$1 AND status IN ('running','paused','failed','success','canceled','rolled_back')`,
		taskID, actorUsername(ctx)); err != nil {
		return res, err
	}

	res.Reset = len(resetIDs)
	if err := tx.Commit(); err != nil {
		return res, err
	}
	return res, nil
}
