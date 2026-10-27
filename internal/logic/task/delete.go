package task

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// MaxBatchDelete 单次批量删除的任务数上限。
const MaxBatchDelete = 5000

// batchDeleteChunk 每个事务处理的任务数：控制单事务锁持有时间与参数规模。
const batchDeleteChunk = 500

// deletableStatus 只允许删除草稿或终态任务。
func deletableStatus(status string) bool {
	switch status {
	case "draft", "success", "failed", "canceled", "rolled_back":
		return true
	default:
		return false
	}
}

// BatchDeleteFailure 批量删除中单个任务的失败原因。
type BatchDeleteFailure struct {
	ID    int64  `json:"id"`
	Error string `json:"error"`
}

// BatchDeleteResult 批量删除结果：成功的 ID 与逐个失败原因。
type BatchDeleteResult struct {
	Deleted []int64              `json:"deleted"`
	Failed  []BatchDeleteFailure `json:"failed"`
}

// BatchDelete 批量删除任务（按集合处理，每批一条 DELETE）。
//
// 校验与单个 Delete 等价：租户隔离、owner 策略、状态（草稿/终态）、无未完成指令。
// 检查从「每个任务若干条 SQL」合并为「每批固定几条 SQL」：
//
//  1. 无锁预查：一次查出租户内存在的任务及 status / created_by，
//     不存在、无权限、状态不可删的直接记为失败，不参与加锁；
//  2. 按 task_batches → task_hosts → commands → upgrade_tasks 顺序批量加锁
//     （与 Cancel / 编排器一致），并按 id 排序，避免并发批量删除互相死锁；
//  3. 锁内复查状态，并一次聚合统计每个任务的未完成指令数；
//  4. 剩余任务用一条 DELETE ... WHERE id = ANY($1) 删除，子表经 ON DELETE CASCADE 清理。
//
// 每 batchDeleteChunk 个任务一个事务；某批失败只影响该批，结果逐个返回。
func (s *Service) BatchDelete(ctx context.Context, sc scope.Scope, ids []int64) (*BatchDeleteResult, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("请选择要删除的任务")
	}
	if len(ids) > MaxBatchDelete {
		return nil, fmt.Errorf("单次最多删除 %d 个任务", MaxBatchDelete)
	}
	// 去重并过滤非法 ID，保留输入顺序用于返回失败列表。
	order := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		order = append(order, id)
	}
	res := &BatchDeleteResult{Deleted: []int64{}, Failed: []BatchDeleteFailure{}}
	if len(order) == 0 {
		return res, nil
	}

	// 按 id 升序分批加锁：所有批量删除使用同一加锁顺序。
	sorted := append([]int64(nil), order...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	checkOwner := s.ownerChecker(ctx)
	failures := make(map[int64]string, 8)
	for start := 0; start < len(sorted); start += batchDeleteChunk {
		end := start + batchDeleteChunk
		if end > len(sorted) {
			end = len(sorted)
		}
		chunk := sorted[start:end]
		if err := ctx.Err(); err != nil {
			for _, id := range chunk {
				failures[id] = err.Error()
			}
			continue
		}
		deleted, err := s.deleteChunk(ctx, sc, chunk, checkOwner, failures)
		if err != nil {
			// 事务级错误：本批未成功删除的任务统一记为失败（已记录的具体原因保留）。
			for _, id := range chunk {
				if _, ok := failures[id]; !ok {
					failures[id] = err.Error()
				}
			}
			continue
		}
		res.Deleted = append(res.Deleted, deleted...)
	}
	for _, id := range order {
		if msg, ok := failures[id]; ok {
			res.Failed = append(res.Failed, BatchDeleteFailure{ID: id, Error: msg})
		}
	}
	sort.Slice(res.Deleted, func(i, j int) bool { return res.Deleted[i] < res.Deleted[j] })
	return res, nil
}

// deleteChunk 在单个事务内删除一批任务，返回实际删除的 ID；
// 逐个不满足条件的任务写入 failures，不会导致整批失败。
func (s *Service) deleteChunk(ctx context.Context, sc scope.Scope, ids []int64,
	checkOwner func(string) error, failures map[int64]string) ([]int64, error) {
	tCond, tArgs := sc.Where("", 2)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 1. 无锁预查：跨租户任务查不到，等同“不存在”，不会锁住他人数据。
	type taskInfo struct {
		status, createdBy string
	}
	found := make(map[int64]taskInfo, len(ids))
	rows, err := tx.QueryContext(ctx,
		`SELECT id, status, COALESCE(created_by,'') FROM upgrade_tasks
		  WHERE id = ANY($1) AND `+tCond,
		append([]any{pq.Array(ids)}, tArgs...)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var info taskInfo
		if err := rows.Scan(&id, &info.status, &info.createdBy); err != nil {
			rows.Close()
			return nil, err
		}
		found[id] = info
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	candidates := make([]int64, 0, len(ids))
	for _, id := range ids {
		info, ok := found[id]
		if !ok {
			failures[id] = "任务不存在"
			continue
		}
		if checkOwner != nil {
			if err := checkOwner(info.createdBy); err != nil {
				failures[id] = err.Error()
				continue
			}
		}
		switch {
		case !deletableStatus(info.status):
			failures[id] = fmt.Sprintf("任务状态为 %s，请先取消任务再删除", info.status)
		default:
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return nil, tx.Commit()
	}
	arr := pq.Array(candidates)

	// 2. 批量加锁：顺序与 Cancel / completeBatch / dispatchHost 一致，
	// ORDER BY 保证多个批量删除并发时加锁顺序相同。
	for _, query := range []string{
		`SELECT 1 FROM task_batches WHERE task_id = ANY($1) ORDER BY task_id, id FOR UPDATE`,
		`SELECT 1 FROM task_hosts WHERE task_id = ANY($1) ORDER BY task_id, id FOR UPDATE`,
		`SELECT 1 FROM commands WHERE task_id = ANY($1) ORDER BY task_id, id FOR UPDATE`,
	} {
		lr, err := tx.QueryContext(ctx, query, arr)
		if err != nil {
			return nil, err
		}
		lr.Close()
		if err := lr.Err(); err != nil {
			return nil, err
		}
	}

	// 3. 锁任务并复查状态，防止预查后被启动/暂停。
	locked := make(map[int64]string, len(candidates))
	rows, err = tx.QueryContext(ctx,
		`SELECT id, status FROM upgrade_tasks
		  WHERE id = ANY($1) AND `+tCond+` ORDER BY id FOR UPDATE`,
		append([]any{arr}, tArgs...)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return nil, err
		}
		locked[id] = status
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// 一次聚合统计每个任务的未完成指令数。
	active := map[int64]int{}
	rows, err = tx.QueryContext(ctx, `
		SELECT task_id, COUNT(*) FROM commands
		 WHERE task_id = ANY($1) AND status IN ('pending','dispatched','accepted','running')
		 GROUP BY task_id`, arr)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		active[id] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	final := make([]int64, 0, len(candidates))
	for _, id := range candidates {
		status, ok := locked[id]
		switch {
		case !ok:
			failures[id] = "任务不存在"
		case !deletableStatus(status):
			failures[id] = fmt.Sprintf("任务状态为 %s，请先取消任务再删除", status)
		case active[id] > 0:
			failures[id] = fmt.Sprintf("任务仍有 %d 条未完成指令，请先取消任务", active[id])
		default:
			final = append(final, id)
		}
	}
	if len(final) == 0 {
		return nil, tx.Commit()
	}

	// 4. 一条 DELETE 删除整批，子表经外键 ON DELETE CASCADE 清理。
	deleted := make([]int64, 0, len(final))
	rows, err = tx.QueryContext(ctx,
		`DELETE FROM upgrade_tasks WHERE id = ANY($1) AND `+tCond+` RETURNING id`,
		append([]any{pq.Array(final)}, tArgs...)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		deleted = append(deleted, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deleted, nil
}

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
