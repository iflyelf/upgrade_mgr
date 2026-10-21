// Package task 任务与灰度编排：创建任务→解析目标→分批→生成待下发指令。
package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/artifact"
	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/ipx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// Selector 目标选择器。
type Selector struct {
	IPExpr        string              `json:"ip_expr"`
	ExcludeIPExpr string              `json:"exclude_ip_expr"`
	Filters       map[string][]string `json:"filters"` // app_code/os_family/... → 值列表
}

// Strategy 灰度策略。
type Strategy struct {
	Mode             string `json:"mode"` // canary/count/percent
	CanaryCount      int    `json:"canary_count"`
	BatchSize        int    `json:"batch_size"`
	Concurrency      int    `json:"concurrency"`
	AutoPromote      bool   `json:"auto_promote"`
	HealthWait       string `json:"health_wait"`
	FailureThreshold string `json:"failure_threshold"`
	AutoRollback     bool   `json:"auto_rollback"`
}

// CreateRequest 创建任务请求。
type CreateRequest struct {
	Name        string   `json:"name"`
	ComponentID int64    `json:"component_id"`
	Version     string   `json:"version"`
	Selector    Selector `json:"selector"`
	Strategy    Strategy `json:"strategy"`
	CreatedBy   string   `json:"created_by"`
}

// Task 任务视图。
type Task struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	ComponentID int64     `json:"component_id"`
	Version     string    `json:"version"`
	Status      string    `json:"status"`
	Selector    Selector  `json:"selector"`
	Strategy    Strategy  `json:"strategy"`
	TotalHosts  int       `json:"total_hosts"`
	CreatedAt   time.Time `json:"created_at"`
}

// Service 任务服务。
type Service struct {
	db       *sql.DB
	compRepo *component.Repo
	artRepo  *artifact.Repo
	// allowExecMode 是否允许下发 exec/replace_exec 模式（默认 false，安全）。
	// 用函数在下发时读取，页面修改设置后无需重启即可生效。
	allowExecMode func() bool
}

// NewService 创建任务服务。allowExecMode 为 nil 时视为禁止。
func NewService(db *sql.DB, compRepo *component.Repo, artRepo *artifact.Repo, allowExecMode func() bool) *Service {
	if allowExecMode == nil {
		allowExecMode = func() bool { return false }
	}
	return &Service{db: db, compRepo: compRepo, artRepo: artRepo, allowExecMode: allowExecMode}
}

// PreviewResult 预览命中主机与分批。
type PreviewResult struct {
	TotalHosts int      `json:"total_hosts"`
	Batches    []int    `json:"batches"`    // 各批次主机数
	SampleIPs  []string `json:"sample_ips"` // 前若干个 IP 预览
}

// Preview 解析目标并计算分批，不落库。
func (s *Service) Preview(ctx context.Context, sc scope.Scope, req CreateRequest) (*PreviewResult, error) {
	hosts, err := s.matchHosts(ctx, sc, req.Selector)
	if err != nil {
		return nil, err
	}
	batches := splitBatches(len(hosts), req.Strategy)
	sample := []string{}
	for i, h := range hosts {
		if i >= 20 {
			break
		}
		sample = append(sample, h.ip)
	}
	return &PreviewResult{TotalHosts: len(hosts), Batches: batches, SampleIPs: sample}, nil
}

// Create 创建任务：落库 upgrade_tasks + task_hosts + task_batches。
func (s *Service) Create(ctx context.Context, sc scope.Scope, req CreateRequest) (int64, error) {
	if req.ComponentID == 0 || req.Version == "" {
		return 0, fmt.Errorf("component_id 与 version 必填")
	}
	// 校验组件存在
	if _, err := s.compRepo.Get(ctx, sc, req.ComponentID); err != nil {
		return 0, err
	}
	hosts, err := s.matchHosts(ctx, sc, req.Selector)
	if err != nil {
		return 0, err
	}
	if len(hosts) == 0 {
		return 0, fmt.Errorf("目标选择器未命中任何主机")
	}

	selJSON, _ := json.Marshal(req.Selector)
	stratJSON, _ := json.Marshal(req.Strategy)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var taskID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO upgrade_tasks (name, component_id, version, selector, strategy, status, created_by, tenant_id)
		VALUES ($1,$2,$3,$4,$5,'draft',$6,$7) RETURNING id`,
		req.Name, req.ComponentID, req.Version, selJSON, stratJSON, req.CreatedBy,
		sc.TenantForWrite()).Scan(&taskID)
	if err != nil {
		return 0, err
	}

	// 分批并写 task_hosts
	batchSizes := splitBatches(len(hosts), req.Strategy)
	idx, batchIdx := 0, 0
	for _, size := range batchSizes {
		for j := 0; j < size && idx < len(hosts); j++ {
			h := hosts[idx]
			_, err = tx.ExecContext(ctx, `
				INSERT INTO task_hosts (task_id, host_id, batch_index, status)
				VALUES ($1,$2,$3,'pending') ON CONFLICT (task_id, host_id) DO NOTHING`,
				taskID, h.id, batchIdx)
			if err != nil {
				return 0, err
			}
			idx++
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO task_batches (task_id, batch_index, status, total)
			VALUES ($1,$2,'pending',$3)`, taskID, batchIdx, size)
		if err != nil {
			return 0, err
		}
		batchIdx++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return taskID, nil
}

type hostRow struct {
	id int64
	ip string
}

// matchHosts 解析 selector：IP 段展开 ∩ 分组过滤 − 排除。
func (s *Service) matchHosts(ctx context.Context, sc scope.Scope, sel Selector) ([]hostRow, error) {
	// 构建分组过滤 SQL。第一个条件恒为租户隔离，确保不会漏过滤。
	tCond, tArgs := sc.Where("", 1)
	where := []string{tCond}
	args := append([]any{}, tArgs...)
	i := len(args) + 1
	allowedCols := map[string]bool{
		"app_code": true, "os_family": true, "os_type": true, "account": true,
		"biz_group": true, "department": true, "vendor": true, "arch": true,
	}
	for col, vals := range sel.Filters {
		if !allowedCols[col] || len(vals) == 0 {
			continue
		}
		where = append(where, fmt.Sprintf("%s = ANY($%d)", col, i))
		args = append(args, pqStringArray(vals))
		i++
	}

	q := "SELECT id, ip FROM hosts WHERE " + join(where, " AND ")
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byIP := map[string]int64{}
	for rows.Next() {
		var id int64
		var ip string
		if err := rows.Scan(&id, &ip); err != nil {
			return nil, err
		}
		byIP[ip] = id
	}

	// IP 段展开（若指定）
	var wantIPs map[string]struct{}
	if sel.IPExpr != "" {
		ips, err := ipx.ParseWithExclude(sel.IPExpr, sel.ExcludeIPExpr, 0)
		if err != nil {
			return nil, fmt.Errorf("IP 表达式解析失败: %w", err)
		}
		wantIPs = map[string]struct{}{}
		for _, ip := range ips {
			wantIPs[ip] = struct{}{}
		}
	}

	var out []hostRow
	for ip, id := range byIP {
		if wantIPs != nil {
			if _, ok := wantIPs[ip]; !ok {
				continue
			}
		}
		out = append(out, hostRow{id: id, ip: ip})
	}
	// 稳定排序（按 IP）
	sortHosts(out)
	return out, nil
}

// splitBatches 依策略计算各批次大小。
func splitBatches(total int, st Strategy) []int {
	if total == 0 {
		return nil
	}
	var batches []int
	remaining := total
	// 金丝雀首批
	if st.Mode == "canary" && st.CanaryCount > 0 && st.CanaryCount < total {
		batches = append(batches, st.CanaryCount)
		remaining -= st.CanaryCount
	}
	// 常规批
	batchSize := st.BatchSize
	if st.Mode == "percent" && st.BatchSize > 0 {
		batchSize = total * st.BatchSize / 100
		// 百分比模式下，批次大小至少为 1（避免 total=3, 20% → 0 的情况）
		if batchSize < 1 {
			batchSize = 1
		}
	}
	if batchSize <= 0 {
		batchSize = remaining // 一批全部
	}
	for remaining > 0 {
		n := batchSize
		if n > remaining {
			n = remaining
		}
		batches = append(batches, n)
		remaining -= n
	}
	return batches
}

// pqStringArray 包装为 pq 数组（避免直接依赖 lib/pq 于签名）。
func pqStringArray(vals []string) any {
	return arrayWrapper(vals)
}
