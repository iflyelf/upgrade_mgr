// Package userfieldsync 用户字段定义的自动同步（对齐 consul_mgr）。
//
// 数据流：FlyIAM /api/user-fields/export → 本地 user_field_defs（仅定义）。
// 本系统不写回 FlyIAM/Casdoor：字段值（人事属性）由 FlyIAM 同步到 Casdoor
// properties，upgrade_mgr 只读展示。
package userfieldsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/fielddef"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/flyiam"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/pglock"
)

const (
	configID = 1
	// lockKey 跨副本互斥键（任意稳定 int64 即可，避免与其它模块冲突）。
	lockKey = int64(0x55534649454C4453) // "USFIELDS"
)

// ErrRunning 已有同步在执行。
var ErrRunning = errors.New("同步正在进行中，请稍后再试")

// Config 自动同步配置。
type Config struct {
	ID            int64      `json:"id"`
	Enabled       bool       `json:"enabled"`
	Interval      string     `json:"interval"`
	SyncOnStartup bool       `json:"sync_on_startup"`
	LastRunAt     *time.Time `json:"lastRunAt"`
	LastStatus    string     `json:"lastStatus"`
	LastMessage   string     `json:"lastMessage"`
}

// Progress 当前同步进度（内存，供页面轮询）。
type Progress struct {
	Running    bool       `json:"running"`
	Status     string     `json:"status"`
	Message    string     `json:"message"`
	Total      int        `json:"total"`
	Added      int        `json:"added"`
	Updated    int        `json:"updated"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// LogEntry 同步历史。
type LogEntry struct {
	ID          int64      `json:"id"`
	Status      string     `json:"status"`
	TriggerType string     `json:"triggerType"`
	Total       int        `json:"total"`
	Added       int        `json:"added"`
	Updated     int        `json:"updated"`
	Message     string     `json:"message"`
	StartedAt   time.Time  `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt"`
}

// Result 单次同步结果。
type Result struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Total   int `json:"total"`
}

// Service 自动同步服务。
type Service struct {
	db *sql.DB
	// cfgFn 运行时读取配置快照（FlyIAM 地址/令牌可在页面热更新）。
	cfgFn func() *config.Config

	runMu    sync.Mutex
	progMu   sync.RWMutex
	progress Progress
}

// New 创建同步服务。
func New(db *sql.DB, cfgFn func() *config.Config) *Service {
	return &Service{db: db, cfgFn: cfgFn}
}

// Progress 返回当前进度。
func (s *Service) Progress() Progress {
	s.progMu.RLock()
	defer s.progMu.RUnlock()
	return s.progress
}

func (s *Service) setProgress(p Progress) {
	s.progMu.Lock()
	s.progress = p
	s.progMu.Unlock()
}

// GetConfig 读取配置；无记录时返回默认值（关闭自动同步、6h）。
func (s *Service) GetConfig(ctx context.Context) (*Config, error) {
	var cfg Config
	var lastRun sql.NullTime
	var lastStatus, lastMessage sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, enabled, interval, sync_on_startup, last_run_at, last_status, last_message
		FROM userfield_sync_config WHERE id=$1`, configID).
		Scan(&cfg.ID, &cfg.Enabled, &cfg.Interval, &cfg.SyncOnStartup, &lastRun, &lastStatus, &lastMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return &Config{ID: configID, Enabled: false, Interval: "6h", SyncOnStartup: false}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取同步配置失败: %w", err)
	}
	if lastRun.Valid {
		t := lastRun.Time
		cfg.LastRunAt = &t
	}
	cfg.LastStatus = lastStatus.String
	cfg.LastMessage = lastMessage.String
	return &cfg, nil
}

// UpdateConfig 保存配置。
func (s *Service) UpdateConfig(ctx context.Context, cfg *Config) error {
	interval := strings.TrimSpace(cfg.Interval)
	if interval == "" {
		interval = "6h"
	}
	if _, err := ParseInterval(interval); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO userfield_sync_config (id, enabled, interval, sync_on_startup, updated_at)
		VALUES ($1,$2,$3,$4,NOW())
		ON CONFLICT (id) DO UPDATE SET
			enabled=EXCLUDED.enabled, interval=EXCLUDED.interval,
			sync_on_startup=EXCLUDED.sync_on_startup, updated_at=NOW()`,
		configID, cfg.Enabled, interval, cfg.SyncOnStartup)
	if err != nil {
		return fmt.Errorf("保存同步配置失败: %w", err)
	}
	cfg.Interval = interval
	return nil
}

// ParseInterval 解析同步间隔（支持 h/m/s/d）。
func ParseInterval(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("同步间隔不能为空")
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("无效的同步间隔: %s", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("无效的同步间隔: %s（示例: 30m、6h、1d）", s)
	}
	return d, nil
}

// Logs 查询最近 N 条同步日志。
func (s *Service) Logs(ctx context.Context, limit int) ([]*LogEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, status, trigger_type, total, added, updated, message, started_at, completed_at
		FROM userfield_sync_logs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询同步日志失败: %w", err)
	}
	defer rows.Close()
	out := make([]*LogEntry, 0, limit)
	for rows.Next() {
		var e LogEntry
		var completed sql.NullTime
		if err := rows.Scan(&e.ID, &e.Status, &e.TriggerType, &e.Total, &e.Added,
			&e.Updated, &e.Message, &e.StartedAt, &completed); err != nil {
			return nil, err
		}
		if completed.Valid {
			t := completed.Time
			e.CompletedAt = &t
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// DeleteLogs 删除同步日志：all=true 清空全部，否则按 ID 批量删除。
func (s *Service) DeleteLogs(ctx context.Context, ids []int64, all bool) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if all {
		res, err = s.db.ExecContext(ctx, `DELETE FROM userfield_sync_logs`)
	} else {
		if len(ids) == 0 {
			return 0, fmt.Errorf("未选择要删除的日志")
		}
		if len(ids) > 500 {
			return 0, fmt.Errorf("一次最多删除 500 条日志")
		}
		res, err = s.db.ExecContext(ctx,
			`DELETE FROM userfield_sync_logs WHERE id = ANY($1)`, pq.Array(ids))
	}
	if err != nil {
		return 0, fmt.Errorf("删除同步日志失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Run 执行一次同步。trigger: manual / schedule / startup。
func (s *Service) Run(ctx context.Context, trigger string) (Result, error) {
	// 进程内单飞：同一实例并发点击只执行一次。
	if !s.runMu.TryLock() {
		return Result{}, ErrRunning
	}
	defer s.runMu.Unlock()

	// 跨副本单飞：多副本部署时只让一个实例执行。
	lock, ok, err := pglock.Try(ctx, s.db, lockKey)
	if err != nil {
		return Result{}, fmt.Errorf("获取同步锁失败: %w", err)
	}
	if !ok {
		return Result{}, ErrRunning
	}
	defer lock.Unlock()

	var cfgSnapshot config.Config
	if s.cfgFn != nil {
		if c := s.cfgFn(); c != nil {
			cfgSnapshot = *c
		}
	}
	client := flyiam.NewClient(cfgSnapshot.FlyIAM.Endpoint, cfgSnapshot.FlyIAM.ServiceToken)
	if client == nil {
		return Result{}, fmt.Errorf("未配置 FlyIAM 地址或服务令牌")
	}

	started := time.Now()
	s.setProgress(Progress{Running: true, Status: "running", Message: "正在从 FlyIAM 拉取字段定义…", StartedAt: &started})
	logID := s.startLog(ctx, trigger)

	finish := func(res Result, err error) (Result, error) {
		now := time.Now()
		p := s.Progress()
		p.Running = false
		p.FinishedAt = &now
		p.Total, p.Added, p.Updated = res.Total, res.Added, res.Updated
		if err != nil {
			p.Status = "failed"
			p.Message = err.Error()
		} else {
			p.Status = "success"
			p.Message = fmt.Sprintf("新增 %d，更新 %d，共 %d 个字段", res.Added, res.Updated, res.Total)
		}
		s.setProgress(p)
		s.finishLog(ctx, logID, p.Status, res, p.Message)
		s.updateLastRun(ctx, p.Status, p.Message)
		return res, err
	}

	remote, err := client.FetchUserFields(ctx)
	if err != nil {
		return finish(Result{}, fmt.Errorf("从 FlyIAM 拉取字段失败: %w", err))
	}
	added, updated, total, err := fielddef.SyncUserDefs(ctx, s.db, remote)
	if err != nil {
		return finish(Result{Added: added, Updated: updated, Total: total}, err)
	}
	return finish(Result{Added: added, Updated: updated, Total: total}, nil)
}

// Start 启动后台调度：启动时可选同步一次，之后按配置间隔执行。
func (s *Service) Start(ctx context.Context) {
	// 启动时同步（延迟 5s，等待服务就绪）。
	if cfg, err := s.GetConfig(ctx); err == nil && cfg.SyncOnStartup {
		go func() {
			time.Sleep(5 * time.Second)
			defer func() {
				if r := recover(); r != nil {
					log.Printf("💥 启动时字段同步 panic（已恢复）: %v\n%s", r, debug.Stack())
				}
			}()
			if _, err := s.Run(ctx, "startup"); err != nil && !errors.Is(err, ErrRunning) {
				log.Printf("⚠️ 启动时同步用户字段失败: %v", err)
			}
		}()
	}

	go func() {
		for {
			cfg, err := s.GetConfig(ctx)
			if err != nil {
				log.Printf("⚠️ 读取字段同步配置失败: %v", err)
			}
			// 固定 30s 轮询一次：配置修改（启用/间隔）最多 30s 生效，
			// 是否到期由 last_run_at + interval 判断。
			wait := 30 * time.Second
			if err == nil && cfg.Enabled {
				if d, perr := ParseInterval(cfg.Interval); perr == nil && cfg.LastRunAt != nil {
					if remaining := time.Until(cfg.LastRunAt.Add(d)); remaining > 0 && remaining < wait {
						wait = remaining
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			// 等待结束后重新读取配置：页面修改启用状态/间隔无需重启即可生效。
			latest, lerr := s.GetConfig(ctx)
			if lerr != nil || !latest.Enabled {
				continue
			}
			d, derr := ParseInterval(latest.Interval)
			if derr != nil {
				continue
			}
			if latest.LastRunAt != nil && time.Since(*latest.LastRunAt) < d {
				continue
			}
			// panic 兜底：后台 goroutine 内 panic 会终止整个进程。
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("💥 定时字段同步 panic（已恢复）: %v\n%s", r, debug.Stack())
					}
				}()
				if _, err := s.Run(ctx, "schedule"); err != nil && !errors.Is(err, ErrRunning) {
					log.Printf("⚠️ 定时同步用户字段失败: %v", err)
				}
			}()
		}
	}()
}

func (s *Service) startLog(ctx context.Context, trigger string) int64 {
	var id int64
	if err := s.db.QueryRowContext(ctx, `
		INSERT INTO userfield_sync_logs (status, trigger_type, started_at)
		VALUES ('running', $1, NOW()) RETURNING id`, trigger).Scan(&id); err != nil {
		log.Printf("⚠️ 写入字段同步日志失败: %v", err)
		return 0
	}
	return id
}

func (s *Service) finishLog(ctx context.Context, id int64, status string, res Result, message string) {
	if id == 0 {
		return
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE userfield_sync_logs
		SET status=$1, total=$2, added=$3, updated=$4, message=$5, completed_at=NOW()
		WHERE id=$6`, status, res.Total, res.Added, res.Updated, message, id); err != nil {
		log.Printf("⚠️ 更新字段同步日志失败: %v", err)
	}
}

func (s *Service) updateLastRun(ctx context.Context, status, message string) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE userfield_sync_config
		SET last_run_at=NOW(), last_status=$1, last_message=$2, updated_at=NOW()
		WHERE id=$3`, status, message, configID); err != nil {
		log.Printf("⚠️ 更新字段同步状态失败: %v", err)
	}
}
