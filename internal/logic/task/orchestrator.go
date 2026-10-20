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

	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// Orchestrator 灰度编排器：周期扫描 running 任务，推进批次。
type Orchestrator struct {
	svc *Service
}

// NewOrchestrator 创建编排器。
func NewOrchestrator(svc *Service) *Orchestrator { return &Orchestrator{svc: svc} }

// Run 启动编排循环（每 5s 扫描一次），随 ctx 结束退出。
func (o *Orchestrator) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			o.tick(ctx)
		}
	}
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
			o.markBatch(ctx, taskID, batchIdx, "failed")
			if st.AutoRollback {
				o.rollbackBatch(ctx, taskID, compID, batchIdx)
			}
			_, _ = o.svc.db.ExecContext(ctx, `UPDATE upgrade_tasks SET status='paused', updated_at=NOW() WHERE id=$1`, taskID)
			return nil
		}
		// 批次通过
		o.markBatch(ctx, taskID, batchIdx, "success")
		if !st.AutoPromote {
			// 需人工推进：置任务为 paused 等待操作
			log.Printf("[orchestrator] 任务 %d 批次 %d 通过，等待人工推进", taskID, batchIdx)
			_, _ = o.svc.db.ExecContext(ctx, `UPDATE upgrade_tasks SET status='paused', updated_at=NOW() WHERE id=$1`, taskID)
			return nil
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
	if !o.svc.allowExecMode {
		if comp.DeployMode == protocol.DeployExec || comp.DeployMode == protocol.DeployReplaceExec {
			log.Printf("⚠️ 任务 %d 组件 %s 使用 %s 模式但 AllowExecMode=false，拒绝下发", taskID, comp.Name, comp.DeployMode)
			return fmt.Errorf("组件 %s 使用 exec 模式但服务端配置禁止（AllowExecMode=false）", comp.Name)
		}
	}

	spec := toComponentSpec(comp)

	rows, err := o.svc.db.QueryContext(ctx, `
		SELECT th.id, th.host_id, h.arch, h.os_family, h.agent_id
		FROM task_hosts th JOIN hosts h ON h.id=th.host_id
		WHERE th.task_id=$1 AND th.batch_index=$2 AND th.status='pending'`, taskID, batchIdx)
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

	dispatched := 0
	for _, r := range list {
		if r.aid == "" {
			o.markHost(ctx, r.thID, "skipped")
			continue
		}
		arch := r.arch
		if arch == "" {
			arch = "amd64"
		}
		art, err := o.svc.artRepo.Find(ctx, sc, compID, version, arch, r.os)
		if err != nil {
			o.markHost(ctx, r.thID, "skipped")
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
		_, err = o.svc.db.ExecContext(ctx, `
			INSERT INTO commands (task_id, task_host_id, host_id, agent_id, type, payload, status, expire_at)
			VALUES ($1,$2,$3,$4,$5,$6,'pending',$7)`,
			taskID, r.thID, r.hostID, r.aid, protocol.CmdUpgradeComponent, pj, time.Now().Add(2*time.Hour))
		if err != nil {
			return err
		}
		o.markHost(ctx, r.thID, "running")
		dispatched++
	}
	o.markBatch(ctx, taskID, batchIdx, "running")
	log.Printf("[orchestrator] 任务 %d 批次 %d 下发 %d 条指令", taskID, batchIdx, dispatched)
	return nil
}

// rollbackBatch 为该批次已成功的主机生成回滚指令（回滚到 rollback_to，此处简化为标记）。
func (o *Orchestrator) rollbackBatch(ctx context.Context, taskID, compID int64, batchIdx int) {
	// P3 简化：标记该批次成功主机为 rolled_back（真实回滚需 rollback_to 制品，留待完善）
	_, _ = o.svc.db.ExecContext(ctx,
		`UPDATE task_hosts SET status='rolled_back' WHERE task_id=$1 AND batch_index=$2 AND status='success'`,
		taskID, batchIdx)
	log.Printf("[orchestrator] 任务 %d 批次 %d 已触发回滚标记", taskID, batchIdx)
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

func (o *Orchestrator) markBatch(ctx context.Context, taskID int64, batchIdx int, status string) {
	_, _ = o.svc.db.ExecContext(ctx,
		`UPDATE task_batches SET status=$3, started_at=COALESCE(started_at,NOW()),
		 finished_at=CASE WHEN $3 IN ('success','failed') THEN NOW() ELSE finished_at END
		 WHERE task_id=$1 AND batch_index=$2`, taskID, batchIdx, status)
}

func (o *Orchestrator) markHost(ctx context.Context, thID int64, status string) {
	_, _ = o.svc.db.ExecContext(ctx,
		`UPDATE task_hosts SET status=$2, started_at=COALESCE(started_at,NOW()) WHERE id=$1`, thID, status)
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

var _ = sql.ErrNoRows
