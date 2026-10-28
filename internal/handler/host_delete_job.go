package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// maxHostDeleteJob 单个异步作业最多删除的主机数（与任务批量删除一致）。
const maxHostDeleteJob = 5000

// hostDeleteJobChunk 每个事务处理的主机数：每台主机一个 SAVEPOINT，单批不宜过大。
const hostDeleteJobChunk = 100

// hostDeleteJobMaxFailures 作业里保留的失败明细上限；总数见 failed_count。
const hostDeleteJobMaxFailures = 200

// hostDeleteJobRetention 已结束作业的保留时长。
const hostDeleteJobRetention = 7 * 24 * time.Hour

// hostDeleteFailure 单台主机删除失败原因。
type hostDeleteFailure struct {
	ID    int64  `json:"id"`
	IP    string `json:"ip,omitempty"`
	Error string `json:"error"`
}

// hostDeleteJob 异步主机删除作业的对外视图（字段与任务删除作业一致，前端可复用）。
type hostDeleteJob struct {
	ID              int64               `json:"id"`
	Status          string              `json:"status"`
	Total           int                 `json:"total"`
	Processed       int                 `json:"processed"`
	Deleted         int                 `json:"deleted"`
	FailedCount     int                 `json:"failed_count"`
	Failed          []hostDeleteFailure `json:"failed"`
	CancelRequested bool                `json:"cancel_requested"`
	CreatedBy       string              `json:"created_by"`
	CreatedAt       time.Time           `json:"created_at"`
	FinishedAt      *time.Time          `json:"finished_at,omitempty"`
}

const hostDeleteJobColumns = `id, status, total, processed, deleted, failed_count, failed,
	cancel_requested, created_by, created_at, finished_at`

func scanHostDeleteJob(row interface{ Scan(...any) error }) (*hostDeleteJob, error) {
	var j hostDeleteJob
	var failed []byte
	var finished sql.NullTime
	if err := row.Scan(&j.ID, &j.Status, &j.Total, &j.Processed, &j.Deleted, &j.FailedCount,
		&failed, &j.CancelRequested, &j.CreatedBy, &j.CreatedAt, &finished); err != nil {
		return nil, err
	}
	j.Failed = []hostDeleteFailure{}
	if len(failed) > 0 {
		_ = json.Unmarshal(failed, &j.Failed)
	}
	if finished.Valid {
		t := finished.Time
		j.FinishedAt = &t
	}
	return &j, nil
}

// actorName 返回当前操作者用户名（未登录为空）。
func actorName(r *http.Request) (string, string) {
	if u, ok := auth.FromContext(r.Context()); ok && u != nil {
		return u.ID, u.Username
	}
	return "", ""
}

// hostJobVisibility 作业可见范围：租户隔离 + 只看自己发起的作业。
// 主机删除本身要求管理员，作业按发起人隔离即可，避免互相停止对方的作业。
func hostJobVisibility(r *http.Request, startIdx int) (string, []any) {
	tCond, tArgs := reqScope(r).Where("", startIdx)
	_, username := actorName(r)
	tArgs = append(tArgs, username)
	return fmt.Sprintf("%s AND created_by=$%d", tCond, startIdx+len(tArgs)-1), tArgs
}

func getHostDeleteJob(r *http.Request, s *svc.ServiceContext, id int64) (*hostDeleteJob, error) {
	cond, args := hostJobVisibility(r, 2)
	j, err := scanHostDeleteJob(s.DB.QueryRowContext(r.Context(),
		`SELECT `+hostDeleteJobColumns+` FROM host_delete_jobs WHERE id=$1 AND `+cond,
		append([]any{id}, args...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("删除作业不存在")
	}
	return j, err
}

// HostDeletableIDs 按当前筛选条件返回可删除的主机 ID（最多 5000）：GET /api/hosts/deletable-ids。
//
// 筛选条件与主机列表完全一致（hostListFilter）；存在未完成任务/指令的主机不返回，
// 删除时服务端仍会逐台复核。truncated=true 表示超过上限，只返回前 5000 台（按 IP 排序）。
func HostDeletableIDs(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		whereSQL, args, i := hostListFilter(s, r)
		args = append(args, maxHostDeleteJob+1)
		rows, err := s.DB.QueryContext(r.Context(), `
			SELECT h.id FROM hosts h`+whereSQL+`
			   AND NOT EXISTS (SELECT 1 FROM task_hosts th
			                    WHERE th.host_id=h.id AND th.status IN ('pending','running'))
			   AND NOT EXISTS (SELECT 1 FROM commands c
			                    WHERE c.host_id=h.id AND c.status IN ('pending','dispatched','accepted','running'))
			 ORDER BY h.ip LIMIT $`+strconv.Itoa(i), args...)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		defer rows.Close()
		ids := []int64{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		truncated := len(ids) > maxHostDeleteJob
		if truncated {
			ids = ids[:maxHostDeleteJob]
		}
		httpx.OK(w, map[string]any{"ids": ids, "truncated": truncated, "limit": maxHostDeleteJob})
	}
}

// HostDeleteJobCreate 创建异步主机删除作业：POST /api/host-delete-jobs {"ids":[...]}。
//
// 立即返回；删除在后台按批执行，校验与单台删除完全一致（deleteHostTx），
// 每台主机单独写审计。同一发起人同时只允许一个未结束作业。
func HostDeleteJobCreate(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []int64 `json:"ids"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		ids := uniquePositiveIDs(req.IDs)
		if len(ids) == 0 {
			httpx.Fail(w, http.StatusBadRequest, "请选择要删除的主机")
			return
		}
		if len(ids) > maxHostDeleteJob {
			httpx.Fail(w, http.StatusBadRequest, fmt.Sprintf("单次最多删除 %d 台主机", maxHostDeleteJob))
			return
		}
		// 按 id 升序处理：多个作业并发时加锁顺序一致。
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		sc := reqScope(r)
		actorID, username := actorName(r)
		var id int64
		err := s.DB.QueryRowContext(r.Context(), `
			INSERT INTO host_delete_jobs (tenant_id, cross_tenant, created_by, actor_id, actor_ip, ids, total)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			sc.TenantForWrite(), sc.CrossTenant, username, actorID, clientIP(s, r),
			pq.Array(ids), len(ids)).Scan(&id)
		if err != nil {
			var pqErr *pq.Error
			if errors.As(err, &pqErr) && pqErr.Code == "23505" {
				httpx.Fail(w, http.StatusConflict, "已有主机删除作业正在执行，请等待完成或先停止")
				return
			}
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		job, err := getHostDeleteJob(r, s, id)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, job)
	}
}

// HostDeleteJobGet 查询作业进度：GET /api/host-delete-jobs/{id}。
func HostDeleteJobGet(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		job, err := getHostDeleteJob(r, s, id)
		if err != nil {
			httpx.FailErr(w, http.StatusNotFound, err)
			return
		}
		httpx.OK(w, job)
	}
}

// HostDeleteJobActive 当前用户未结束的作业（没有时 data 为 null），页面刷新后恢复进度。
func HostDeleteJobActive(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cond, args := hostJobVisibility(r, 1)
		j, err := scanHostDeleteJob(s.DB.QueryRowContext(r.Context(),
			`SELECT `+hostDeleteJobColumns+` FROM host_delete_jobs
			  WHERE status IN ('pending','running') AND `+cond+` ORDER BY id DESC LIMIT 1`, args...))
		if errors.Is(err, sql.ErrNoRows) {
			httpx.OK(w, nil)
			return
		}
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, j)
	}
}

// HostDeleteJobCancel 请求停止作业：当前批完成后生效，已删除的主机不会恢复。
func HostDeleteJobCancel(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		cond, args := hostJobVisibility(r, 2)
		if _, err := s.DB.ExecContext(r.Context(),
			`UPDATE host_delete_jobs SET cancel_requested=TRUE, updated_at=NOW()
			  WHERE id=$1 AND status IN ('pending','running') AND `+cond,
			append([]any{id}, args...)...); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		job, err := getHostDeleteJob(r, s, id)
		if err != nil {
			httpx.FailErr(w, http.StatusNotFound, err)
			return
		}
		httpx.OK(w, job)
	}
}

// StartHostDeleteJobs 启动主机删除作业的后台执行循环（与任务删除作业同一模式）。
//
// 作业行用 FOR UPDATE SKIP LOCKED 认领：多副本各自处理不同作业；
// 每批「删除主机 + 更新进度」在一个事务内提交，进程退出时整批回滚，重启后继续。
func StartHostDeleteJobs(ctx context.Context, s *svc.ServiceContext) {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		lastCleanup := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				did, err := runHostDeleteJobOnce(ctx, s)
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("[host-delete-job] 执行失败: %v", err)
					}
					break
				}
				if !did {
					break
				}
			}
			if time.Since(lastCleanup) > 10*time.Minute {
				lastCleanup = time.Now()
				if _, err := s.DB.ExecContext(ctx, `
					DELETE FROM host_delete_jobs
					 WHERE status IN ('done','canceled') AND finished_at < $1`,
					time.Now().Add(-hostDeleteJobRetention)); err != nil && ctx.Err() == nil {
					log.Printf("[host-delete-job] 清理过期作业失败: %v", err)
				}
			}
		}
	}()
	log.Println("✅ 主机批量删除作业已启动（后台按批执行，多副本 SKIP LOCKED 认领）")
}

// runHostDeleteJobOnce 处理一个作业的一批；没有可处理的作业时返回 false。
func runHostDeleteJobOnce(ctx context.Context, s *svc.ServiceContext) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var (
		jobID, tenantID        int64
		cross, cancel          bool
		createdBy, actorID, ip string
		ids                    []int64
		processed              int
		failedRaw              []byte
	)
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, cross_tenant, created_by, actor_id, actor_ip, ids, processed, failed, cancel_requested
		  FROM host_delete_jobs
		 WHERE status IN ('pending','running')
		 ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(
		&jobID, &tenantID, &cross, &createdBy, &actorID, &ip, pq.Array(&ids), &processed, &failedRaw, &cancel)
	if errors.Is(err, sql.ErrNoRows) {
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
		if _, err := tx.ExecContext(ctx,
			`UPDATE host_delete_jobs SET status=$2, finished_at=NOW(), updated_at=NOW() WHERE id=$1`,
			jobID, status); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}

	end := processed + hostDeleteJobChunk
	if end > len(ids) {
		end = len(ids)
	}
	chunk := ids[processed:end]
	sc := scope.New(tenantID, cross)

	type result struct {
		id  int64
		ip  string
		err *deleteError
	}
	results := make([]result, 0, len(chunk))
	deleted := 0
	var chunkFailed []hostDeleteFailure
	for _, id := range chunk {
		// 每台主机一个 SAVEPOINT：单台失败只回滚该台，不影响同批其他主机。
		if _, err := tx.ExecContext(ctx, `SAVEPOINT host_del`); err != nil {
			return false, err
		}
		hip, derr := deleteHostTx(ctx, tx, sc, id)
		if derr != nil {
			if ctx.Err() != nil {
				return false, ctx.Err() // 服务退出：整批回滚，重启后重试本批
			}
			if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT host_del`); err != nil {
				return false, err
			}
			chunkFailed = append(chunkFailed, hostDeleteFailure{ID: id, IP: hip, Error: derr.msg})
		} else {
			if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT host_del`); err != nil {
				return false, err
			}
			deleted++
		}
		results = append(results, result{id: id, ip: hip, err: derr})
	}

	stored := []hostDeleteFailure{}
	if len(failedRaw) > 0 {
		_ = json.Unmarshal(failedRaw, &stored)
	}
	for _, f := range chunkFailed {
		if len(stored) >= hostDeleteJobMaxFailures {
			break
		}
		stored = append(stored, f)
	}
	storedJSON, _ := json.Marshal(stored)
	status := "running"
	var finished any
	if end >= len(ids) {
		status = "done"
		finished = time.Now()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE host_delete_jobs
		   SET processed=$2, deleted=deleted+$3, failed_count=failed_count+$4, failed=$5,
		       status=$6, finished_at=$7, updated_at=NOW()
		 WHERE id=$1`,
		jobID, end, deleted, len(chunkFailed), storedJSON, status, finished); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}

	// 提交后逐台写审计（与同步删除一致），并清 HTTP 缓存让列表及时刷新。
	if s.Audit != nil {
		for _, res := range results {
			ev := audit.Event{
				Action: audit.ActionHostDelete, UserID: actorID, Username: createdBy, TenantID: tenantID,
				IP: ip, ResourceType: "host", ResourceID: strconv.FormatInt(res.id, 10), ResourceName: res.ip,
				Success: res.err == nil, Details: map[string]any{"host_delete_job_id": jobID},
			}
			if res.err != nil {
				ev.Message = res.err.Error()
			}
			s.Audit.Log(ctx, ev)
		}
	}
	if deleted > 0 {
		if c := s.GetCache(); c != nil {
			cctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			c.DelPrefix(cctx, "http")
			cancelFn()
		}
	}
	return true, nil
}
