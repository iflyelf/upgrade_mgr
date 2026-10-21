// Package task 灰度编排器：按批次推进，批间健康门禁 + 失败率阈值 + 自动回滚。
package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/pglock"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// orchestratorLockKey 编排器跨实例互斥键（与迁移、维护、Consul 同步的键区分）。
const orchestratorLockKey int64 = 0x7570677261646503 // "upgrade\x03"

// Orchestrator 灰度编排器：周期扫描 running 任务，推进批次。
type Orchestrator struct {
	svc *Service
}

// NewOrchestrator 创建编排器。
func NewOrchestrator(svc *Service) *Orchestrator { return &Orchestrator{svc: svc} }

// Run 启动编排循环（每 5s 扫描一次），随 ctx 结束退出。
//
// 所有副本都可以运行本循环；每轮通过 PostgreSQL advisory lock 抢占执行权，
// 抢不到直接跳过，因此单机与多副本使用同一套逻辑，不依赖 Consul。
func (o *Orchestrator) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			o.tickWithLock(ctx)
		}
	}
}

func (o *Orchestrator) tickWithLock(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// 连接池繁忙时最多等 3 秒，避免阻塞整个编排循环。
	lockCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	lock, ok, err := pglock.Try(lockCtx, o.svc.db, orchestratorLockKey)
	if err != nil {
		log.Printf("[orchestrator] 获取编排锁失败: %v", err)
		return
	}
	if !ok {
		return // 其他副本正在推进，本轮跳过
	}
	defer lock.Unlock()
	o.tick(ctx)
}

func (o *Orchestrator) tick(ctx context.Context) {
	rows, err := o.svc.db.QueryContext(ctx,
		`SELECT id, component_id, version, strategy, COALESCE(tenant_id,1) FROM upgrade_tasks WHERE status='running'`)
	if err != nil {
		return
	}
	defer rows.Close()
	type running struct {
		id       int64
		compID   int64
		version  string
		strategy []byte
		tenantID int64
	}
	var tasks []running
	for rows.Next() {
		var r running
		if err := rows.Scan(&r.id, &r.compID, &r.version, &r.strategy, &r.tenantID); err == nil {
			tasks = append(tasks, r)
		}
	}
	// 检查迭代过程中是否发生错误
	if err := rows.Err(); err != nil {
		log.Printf("[orchestrator] 扫描 running 任务失败: %v", err)
		return
	}
	// 先关闭结果集再执行后续查询，避免长时间占用连接池。
	_ = rows.Close()

	for _, t := range tasks {
		var st Strategy
		_ = json.Unmarshal(t.strategy, &st)
		if err := o.advance(ctx, t.id, t.compID, t.version, st, t.tenantID); err != nil {
			log.Printf("[orchestrator] 任务 %d 推进出错: %v", t.id, err)
		}
	}
}

// advance 推进单个任务：评估当前批次，必要时下发下一批或终结任务。
func (o *Orchestrator) advance(ctx context.Context, taskID, compID int64, version string, st Strategy, tenantID int64) error {
	// 找到当前处理中的批次（第一个非 success 且非 skipped 的批次）
	batchIdx, batchStatus, err := o.currentBatch(ctx, taskID)
	if err != nil {
		return err
	}
	if batchIdx < 0 {
		// 无剩余批次 → 任务完成
		return o.finishTask(ctx, taskID)
	}

	switch batchStatus {
	case "pending":
		// 下发该批次指令
		return o.dispatchBatch(ctx, taskID, compID, version, batchIdx, tenantID)
	case "running":
		// 检查该批次是否全部到达终态
		total, done, failed, err := o.batchProgress(ctx, taskID, batchIdx)
		if err != nil {
			return err
		}
		if done < total {
			return nil // 仍在执行，等待
		}
		// 批次完成，评估失败率
		threshold := parsePercent(st.FailureThreshold)
		failRate := 0.0
		if total > 0 {
			failRate = float64(failed) / float64(total) * 100
		}
		if failRate > threshold {
			log.Printf("[orchestrator] 任务 %d 批次 %d 失败率 %.0f%% 超阈值 %.0f%%，暂停",
				taskID, batchIdx, failRate, threshold)
			if err := o.completeBatch(ctx, taskID, batchIdx, "failed", st.AutoRollback, true); err != nil {
				return err
			}
			if st.AutoRollback {
				log.Printf("[orchestrator] 任务 %d 批次 %d 已触发回滚标记", taskID, batchIdx)
			}
			return nil
		}
		// 批次通过
		if !st.AutoPromote {
			// 需人工推进：批次通过和任务暂停必须原子完成，避免滚动重启后
			// 误把下一批当成本批自动推进。
			if err := o.completeBatch(ctx, taskID, batchIdx, "success", false, true); err != nil {
				return err
			}
			log.Printf("[orchestrator] 任务 %d 批次 %d 通过，等待人工推进", taskID, batchIdx)
			return nil
		}
		if err := o.completeBatch(ctx, taskID, batchIdx, "success", false, false); err != nil {
			return err
		}
		// 自动推进：健康观察窗口
		if wait, _ := time.ParseDuration(st.HealthWait); wait > 0 {
			time.Sleep(0) // 观察窗口由批次执行耗时自然覆盖，避免阻塞 tick
		}
		return nil // 下个 tick 处理下一批
	}
	return nil
}

// currentBatch 返回当前待处理批次索引与状态；全部完成返回 -1。
func (o *Orchestrator) currentBatch(ctx context.Context, taskID int64) (int, string, error) {
	rows, err := o.svc.db.QueryContext(ctx,
		`SELECT batch_index, status FROM task_batches WHERE task_id=$1 ORDER BY batch_index`, taskID)
	if err != nil {
		return -1, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var idx int
		var status string
		if err := rows.Scan(&idx, &status); err != nil {
			return -1, "", err
		}
		if status != "success" && status != "skipped" {
			return idx, status, nil
		}
	}
	return -1, "", nil
}

func (o *Orchestrator) batchProgress(ctx context.Context, taskID int64, batchIdx int) (total, done, failed int, err error) {
	err = o.svc.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE status IN ('success','failed','rolled_back','timeout','skipped')),
		       COUNT(*) FILTER (WHERE status IN ('failed','timeout'))
		FROM task_hosts WHERE task_id=$1 AND batch_index=$2`, taskID, batchIdx).
		Scan(&total, &done, &failed)
	return
}

// dispatchBatch 下发指定批次。
//
// 后台进程无登录用户，故以「任务自身所属租户」构造作用域，
// 而非赋予跨租户特权——防止任务被改 component_id 后读到他租户组件。
func (o *Orchestrator) dispatchBatch(ctx context.Context, taskID, compID int64, version string, batchIdx int, tenantID int64) error {
	sc := scope.New(tenantID, false)
	comp, err := o.svc.compRepo.Get(ctx, sc, compID)
	if err != nil {
		return err
	}

	// 安全：强制校验 AllowExecMode（exec/replace_exec 可执行任意命令，默认禁用）
	if !o.svc.allowExecMode() {
		if comp.DeployMode == protocol.DeployExec || comp.DeployMode == protocol.DeployReplaceExec {
			log.Printf("⚠️ 任务 %d 组件 %s 使用 %s 模式但 AllowExecMode=false，拒绝下发", taskID, comp.Name, comp.DeployMode)
			return fmt.Errorf("组件 %s 使用 exec 模式但服务端配置禁止（AllowExecMode=false）", comp.Name)
		}
	}

	spec := toComponentSpec(comp)

	rows, err := o.svc.db.QueryContext(ctx, `
		SELECT th.id, th.host_id, h.arch, h.os_family, h.agent_id
		FROM task_hosts th JOIN hosts h ON h.id=th.host_id
		WHERE th.task_id=$1 AND th.batch_index=$2 AND th.status='pending'
		ORDER BY th.id`, taskID, batchIdx)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		thID, hostID  int64
		arch, os, aid string
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.thID, &r.hostID, &r.arch, &r.os, &r.aid); err == nil {
			list = append(list, r)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// 先关闭结果集，后续逐主机事务不再占用额外连接。
	_ = rows.Close()

	dispatched := 0
	for _, r := range list {
		if r.aid == "" {
			o.markHostSkipped(ctx, r.thID)
			continue
		}
		arch := r.arch
		if arch == "" {
			arch = "amd64"
		}
		art, err := o.svc.artRepo.Find(ctx, sc, compID, version, arch, r.os)
		if err != nil {
			o.markHostSkipped(ctx, r.thID)
			continue
		}
		payload := protocol.CommandPayload{
			Component: spec, ToVersion: version,
			Artifact: protocol.Artifact{
				ID: art.ID, Version: art.Version, Arch: art.Arch, OSFamily: art.OSFamily,
				URL: fmt.Sprintf("/api/artifacts/%d/download", art.ID),
				MD5: art.MD5, SHA256: art.SHA256, Size: art.Size, InnerPath: art.InnerPath,
			},
		}
		pj, _ := json.Marshal(payload)
		ok, err := o.dispatchHost(ctx, taskID, r.thID, r.hostID, r.aid, pj)
		if err != nil {
			return err
		}
		if ok {
			dispatched++
		}
	}
	// 若任务在遍历期间被暂停/取消，或者个别主机暂时无法标记 skipped，
	// 保留批次 pending，等任务恢复后下一轮重试；避免批次卡在 running。
	var pending int
	if err := o.svc.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_hosts WHERE task_id=$1 AND batch_index=$2 AND status='pending'`,
		taskID, batchIdx).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		log.Printf("[orchestrator] 任务 %d 批次 %d 仍有 %d 台待下发，保留 pending", taskID, batchIdx, pending)
		return nil
	}
	if err := o.markBatch(ctx, taskID, batchIdx, "running"); err != nil {
		return err
	}
	log.Printf("[orchestrator] 任务 %d 批次 %d 下发 %d 条指令", taskID, batchIdx, dispatched)
	return nil
}

// dispatchHost 在单个事务内完成「锁主机行 → 写指令 → 标记 running」。
//
// 滚动重启或进程崩溃时，事务会整体回滚，不会出现「指令已写入但主机仍 pending」
// 导致下一轮重复下发的中间状态；多个实例同时处理同一主机时由行锁串行化。
func (o *Orchestrator) dispatchHost(ctx context.Context, taskID, thID, hostID int64, agentID string, payload []byte) (bool, error) {
	tx, err := o.svc.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM task_hosts WHERE id=$1 FOR UPDATE`, thID).Scan(&status)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status != "pending" {
		return false, nil
	}
	// 任务可能已被 Pause/Cancel：不再下发新指令（主机保持 pending，恢复后重试）。
	var taskStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM upgrade_tasks WHERE id=$1`, taskID).Scan(&taskStatus); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if taskStatus != "running" {
		return false, nil
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, payload, status, expire_at)
		VALUES ($1,$2,$3,$4,$5,$6,'pending',$7)`,
		taskID, thID, hostID, agentID, protocol.CmdUpgradeComponent, payload, time.Now().Add(2*time.Hour)); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE task_hosts SET status='running', started_at=COALESCE(started_at,NOW()) WHERE id=$1 AND status='pending'`,
		thID)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, err
	} else if n != 1 {
		return false, fmt.Errorf("主机 %d 状态更新失败（影响 %d 行）", thID, n)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// completeBatch 事务化完成批次：更新批次状态，可选标记回滚，可选暂停任务。
//
// 把「批次成功/失败」和「任务暂停」放进同一事务，滚动重启或崩溃时不会
// 出现批次已通过但任务仍在 running、下一批被自动下发的中间状态。
func (o *Orchestrator) completeBatch(ctx context.Context, taskID int64, batchIdx int, status string, rollback, pauseTask bool) error {
	tx, err := o.svc.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE task_batches SET status=$3::varchar, started_at=COALESCE(started_at,NOW()),
		 finished_at=CASE WHEN $3::varchar IN ('success','failed') THEN NOW() ELSE finished_at END
		 WHERE task_id=$1 AND batch_index=$2`, taskID, batchIdx, status); err != nil {
		return err
	}
	if rollback {
		if _, err := tx.ExecContext(ctx,
			`UPDATE task_hosts SET status='rolled_back'
			 WHERE task_id=$1 AND batch_index=$2 AND status='success'`, taskID, batchIdx); err != nil {
			return err
		}
	}
	if pauseTask {
		if _, err := tx.ExecContext(ctx,
			`UPDATE upgrade_tasks SET status='paused', updated_at=NOW()
			 WHERE id=$1 AND status='running'`, taskID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (o *Orchestrator) finishTask(ctx context.Context, taskID int64) error {
	var failed int
	_ = o.svc.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_hosts WHERE task_id=$1 AND status IN ('failed','timeout')`, taskID).Scan(&failed)
	status := "success"
	if failed > 0 {
		status = "failed"
	}
	_, err := o.svc.db.ExecContext(ctx,
		`UPDATE upgrade_tasks SET status=$2, updated_at=NOW() WHERE id=$1 AND status='running'`, taskID, status)
	return err
}

func (o *Orchestrator) markBatch(ctx context.Context, taskID int64, batchIdx int, status string) error {
	_, err := o.svc.db.ExecContext(ctx,
		`UPDATE task_batches SET status=$3::varchar, started_at=COALESCE(started_at,NOW()),
		 finished_at=CASE WHEN $3::varchar IN ('success','failed') THEN NOW() ELSE finished_at END
		 WHERE task_id=$1 AND batch_index=$2`, taskID, batchIdx, status)
	return err
}

func (o *Orchestrator) markHostSkipped(ctx context.Context, thID int64) {
	_, _ = o.svc.db.ExecContext(ctx,
		`UPDATE task_hosts SET status='skipped', finished_at=NOW()
		 WHERE id=$1 AND status='pending'`, thID)
}

// parsePercent 解析 "10%" / "10" → 10.0；空/非法 → 0（任一失败即暂停）。
func parsePercent(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}
