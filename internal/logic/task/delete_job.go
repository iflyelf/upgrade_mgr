package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// maxJobFailureDetails 作业里保留的失败明细上限；总数见 FailedCount。
const maxJobFailureDetails = 200

// deleteJobRetention 已结束作业的保留时长，过期后由后台清理。
const deleteJobRetention = 7 * 24 * time.Hour

// DeleteJob 异步批量删除作业的对外视图。
type DeleteJob struct {
	ID              int64                `json:"id"`
	Status          string               `json:"status"` // pending/running/done/canceled
	Total           int                  `json:"total"`
	Processed       int                  `json:"processed"`
	Deleted         int                  `json:"deleted"`
	FailedCount     int                  `json:"failed_count"`
	Failed          []BatchDeleteFailure `json:"failed"`
	CancelRequested bool                 `json:"cancel_requested"`
	CreatedBy       string               `json:"created_by"`
	CreatedAt       time.Time            `json:"created_at"`
	FinishedAt      *time.Time           `json:"finished_at,omitempty"`
}

// DeleteJobHook 每批删除提交后回调：用于逐任务审计与缓存失效。
// actor 为作业发起人身份快照，ip 为发起时的客户端地址。
type DeleteJobHook func(ctx context.Context, jobID int64, actor *auth.User, ip string,
	deleted []int64, failed []BatchDeleteFailure)

// SetDeleteJobHook 注入删除作业回调（由服务端启动时设置）。
func (s *Service) SetDeleteJobHook(fn DeleteJobHook) { s.deleteJobHook = fn }

// CreateDeleteJob 创建异步删除作业，立即返回；删除由 RunDeleteJobs 在后台按批执行。
//
// 发起人身份（租户、角色）随作业持久化，后台执行时据此重建作用域与 owner 校验，
// 校验规则与同步 BatchDelete 完全一致。同一发起人同时只允许一个未结束作业。
func (s *Service) CreateDeleteJob(ctx context.Context, sc scope.Scope, ip string, ids []int64) (*DeleteJob, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("请选择要删除的任务")
	}
	if len(ids) > MaxBatchDelete {
		return nil, fmt.Errorf("单次最多删除 %d 个任务", MaxBatchDelete)
	}
	seen := make(map[int64]bool, len(ids))
	uniq := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return nil, fmt.Errorf("请选择要删除的任务")
	}
	// 与同步批量删除一致按 id 升序处理，多个作业并发时加锁顺序相同。
	sort.Slice(uniq, func(i, j int) bool { return uniq[i] < uniq[j] })

	actor, _ := auth.FromContext(ctx)
	var username string
	var isAdmin bool
	roles := []string{}
	if actor != nil {
		username, isAdmin = actor.Username, actor.IsAdmin
		roles = append(roles, actor.Roles...)
	}

	var active int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM task_delete_jobs
		 WHERE tenant_id=$1 AND created_by=$2 AND status IN ('pending','running')
		 ORDER BY id DESC LIMIT 1`, sc.TenantForWrite(), username).Scan(&active)
	if err == nil {
		return nil, fmt.Errorf("已有删除作业 #%d 正在执行，请等待完成或先停止", active)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	var id int64
	if err := s.db.QueryRowContext(ctx, `
		INSERT INTO task_delete_jobs (tenant_id, cross_tenant, created_by, actor_is_admin,
		                              actor_roles, actor_ip, ids, total)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		sc.TenantForWrite(), sc.CrossTenant, username, isAdmin,
		pq.Array(roles), ip, pq.Array(uniq), len(uniq)).Scan(&id); err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return nil, fmt.Errorf("已有删除作业正在执行，请等待完成或先停止")
		}
		return nil, err
	}
	return s.GetDeleteJob(ctx, sc, id)
}

// jobVisibility 作业可见范围：租户隔离；非管理员只能看到自己发起的作业。
func jobVisibility(ctx context.Context, sc scope.Scope, startIdx int) (string, []any) {
	tCond, tArgs := sc.Where("", startIdx)
	actor, _ := auth.FromContext(ctx)
	if isPrivilegedActor(actor) {
		return tCond, tArgs
	}
	tArgs = append(tArgs, actor.Username)
	return fmt.Sprintf("%s AND created_by=$%d", tCond, startIdx+len(tArgs)-1), tArgs
}

const deleteJobColumns = `id, status, total, processed, deleted, failed_count, failed,
	cancel_requested, created_by, created_at, finished_at`

func scanDeleteJob(row interface{ Scan(...any) error }) (*DeleteJob, error) {
	var j DeleteJob
	var failed []byte
	var finished sql.NullTime
	if err := row.Scan(&j.ID, &j.Status, &j.Total, &j.Processed, &j.Deleted, &j.FailedCount,
		&failed, &j.CancelRequested, &j.CreatedBy, &j.CreatedAt, &finished); err != nil {
		return nil, err
	}
	j.Failed = []BatchDeleteFailure{}
	if len(failed) > 0 {
		_ = json.Unmarshal(failed, &j.Failed)
	}
	if finished.Valid {
		t := finished.Time
		j.FinishedAt = &t
	}
	return &j, nil
}

// GetDeleteJob 查询作业进度。
func (s *Service) GetDeleteJob(ctx context.Context, sc scope.Scope, id int64) (*DeleteJob, error) {
	cond, args := jobVisibility(ctx, sc, 2)
	j, err := scanDeleteJob(s.db.QueryRowContext(ctx,
		`SELECT `+deleteJobColumns+` FROM task_delete_jobs WHERE id=$1 AND `+cond,
		append([]any{id}, args...)...))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("删除作业不存在")
	}
	return j, err
}

// ActiveDeleteJob 返回当前用户最近一个未结束的作业；没有时返回 nil。
// 页面刷新后据此恢复进度显示。
func (s *Service) ActiveDeleteJob(ctx context.Context, sc scope.Scope) (*DeleteJob, error) {
	actor, _ := auth.FromContext(ctx)
	username := ""
	if actor != nil {
		username = actor.Username
	}
	tCond, tArgs := sc.Where("", 2)
	j, err := scanDeleteJob(s.db.QueryRowContext(ctx,
		`SELECT `+deleteJobColumns+` FROM task_delete_jobs
		  WHERE created_by=$1 AND status IN ('pending','running') AND `+tCond+`
		  ORDER BY id DESC LIMIT 1`,
		append([]any{username}, tArgs...)...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// CancelDeleteJob 请求停止作业：当前批完成后生效，已删除的任务不会恢复。
func (s *Service) CancelDeleteJob(ctx context.Context, sc scope.Scope, id int64) (*DeleteJob, error) {
	cond, args := jobVisibility(ctx, sc, 2)
	res, err := s.db.ExecContext(ctx,
		`UPDATE task_delete_jobs SET cancel_requested=TRUE, updated_at=NOW()
		  WHERE id=$1 AND status IN ('pending','running') AND `+cond,
		append([]any{id}, args...)...)
	if err != nil {
		return nil, err
	}
	// 已结束或不可见时不更新，直接返回当前状态（不可见时报不存在）。
	_ = res
	return s.GetDeleteJob(ctx, sc, id)
}

// RunDeleteJobs 后台执行删除作业，随 ctx 结束退出。
//
// 每批在一个事务内完成「锁作业行 → 删除本批 → 更新进度」：
// 作业行用 FOR UPDATE SKIP LOCKED 认领，多副本可同时处理不同作业而不会重复处理同一批；
// 进程中途退出时事务回滚，重启后从已提交的进度继续。
func (s *Service) RunDeleteJobs(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	lastCleanup := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// 有作业时连续处理，单轮最多 10 秒，避免长时间占用循环。
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			did, err := s.runDeleteJobOnce(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("[delete-job] 执行失败: %v", err)
				}
				break
			}
			if !did {
				break
			}
		}
		if time.Since(lastCleanup) > 10*time.Minute {
			lastCleanup = time.Now()
			if _, err := s.db.ExecContext(ctx, `
				DELETE FROM task_delete_jobs
				 WHERE status IN ('done','canceled') AND finished_at < $1`,
				time.Now().Add(-deleteJobRetention)); err != nil && ctx.Err() == nil {
				log.Printf("[delete-job] 清理过期作业失败: %v", err)
			}
		}
	}
}

// runDeleteJobOnce 处理一个作业的一批；没有可处理的作业时返回 false。
func (s *Service) runDeleteJobOnce(ctx context.Context) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var (
		jobID, tenantID        int64
		cross, isAdmin, cancel bool
		createdBy, ip          string
		roles                  []string
		ids                    []int64
		processed, total       int
		failedRaw              []byte
	)
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, cross_tenant, created_by, actor_is_admin, actor_roles, actor_ip,
		       ids, processed, total, failed, cancel_requested
		  FROM task_delete_jobs
		 WHERE status IN ('pending','running')
		 ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(
		&jobID, &tenantID, &cross, &createdBy, &isAdmin, pq.Array(&roles), &ip,
		pq.Array(&ids), &processed, &total, &failedRaw, &cancel)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if cancel || processed >= len(ids) {
		status := "done"
		if cancel && processed < len(ids) {
			status = "canceled"
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE task_delete_jobs SET status=$2, finished_at=NOW(), updated_at=NOW() WHERE id=$1`,
			jobID, status); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}

	end := processed + batchDeleteChunk
	if end > len(ids) {
		end = len(ids)
	}
	chunk := ids[processed:end]

	// 以发起人身份执行：作用域与 owner 校验和同步删除一致。
	actor := &auth.User{ID: createdBy, Username: createdBy, IsAdmin: isAdmin, TenantID: tenantID, Roles: roles}
	actx := auth.WithUser(ctx, actor)
	sc := scope.New(tenantID, cross)
	failures := make(map[int64]string, 8)

	// SAVEPOINT：本批删除出错时只回滚删除，进度照常前进（本批记为失败），
	// 避免同一批反复失败导致作业卡死。
	if _, err := tx.ExecContext(ctx, `SAVEPOINT job_chunk`); err != nil {
		return false, err
	}
	deleted, chunkErr := s.deleteChunkTx(actx, tx, sc, chunk, s.ownerChecker(actx), failures)
	if chunkErr != nil {
		if ctx.Err() != nil {
			return false, ctx.Err() // 服务退出：整体回滚，重启后重试本批
		}
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT job_chunk`); err != nil {
			return false, err
		}
		// 回滚后本批没有任何任务被删除：未记录具体原因的任务统一记为本批错误。
		deleted = nil
		for _, id := range chunk {
			if _, ok := failures[id]; !ok {
				failures[id] = chunkErr.Error()
			}
		}
	}

	chunkFailed := make([]BatchDeleteFailure, 0, len(failures))
	for _, id := range chunk {
		if msg, ok := failures[id]; ok {
			chunkFailed = append(chunkFailed, BatchDeleteFailure{ID: id, Error: msg})
		}
	}
	stored := []BatchDeleteFailure{}
	if len(failedRaw) > 0 {
		_ = json.Unmarshal(failedRaw, &stored)
	}
	for _, f := range chunkFailed {
		if len(stored) >= maxJobFailureDetails {
			break
		}
		stored = append(stored, f)
	}
	storedJSON, _ := json.Marshal(stored)

	newProcessed := end
	status := "running"
	var finished any
	if newProcessed >= len(ids) {
		status = "done"
		finished = time.Now()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE task_delete_jobs
		   SET processed=$2, deleted=deleted+$3, failed_count=failed_count+$4, failed=$5,
		       status=$6, finished_at=$7, updated_at=NOW()
		 WHERE id=$1`,
		jobID, newProcessed, len(deleted), len(chunkFailed), storedJSON, status, finished); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if s.deleteJobHook != nil {
		s.deleteJobHook(ctx, jobID, actor, ip, deleted, chunkFailed)
	}
	return true, nil
}
