// Package maintenance 数据库自动维护（定期清理/VACUUM/统计信息更新）。
package maintenance

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// Maintainer 数据库维护器。
type Maintainer struct {
	db *sql.DB
}

// New 创建维护器。
func New(db *sql.DB) *Maintainer {
	return &Maintainer{db: db}
}

// Start 启动自动维护任务（每日凌晨 2 点执行）。
func (m *Maintainer) Start(ctx context.Context) {
	go m.scheduleLoop(ctx)
}

func (m *Maintainer) scheduleLoop(ctx context.Context) {
	// 首次启动延迟到下一个凌晨 2 点
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), 2, 0, 0, 0, now.Location())
	if next.Before(now) {
		next = next.Add(24 * time.Hour)
	}

	timer := time.NewTimer(time.Until(next))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.runMaintenance(ctx)
			// 下次执行时间
			timer.Reset(24 * time.Hour)
		}
	}
}

// maintLockKey 维护任务 advisory lock 键（多实例仅一个执行维护）。
const maintLockKey int64 = 0x7570677261646502 // "upgrade\x02"

// runMaintenance 执行维护任务。
//
// 多实例场景：用 pg_try_advisory_lock 抢占，抢不到说明其他实例正在维护，
// 本实例直接跳过（非阻塞），避免重复 VACUUM/清理。
func (m *Maintainer) runMaintenance(ctx context.Context) {
	conn, err := m.db.Conn(ctx)
	if err != nil {
		log.Printf("⚠️ 维护：获取连接失败，跳过本轮: %v", err)
		return
	}
	defer conn.Close()

	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", maintLockKey).Scan(&got); err != nil {
		log.Printf("⚠️ 维护：抢锁失败，跳过本轮: %v", err)
		return
	}
	if !got {
		log.Println("ℹ️ 维护：其他实例正在执行，本实例跳过")
		return
	}
	defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", maintLockKey)

	log.Println("🔧 开始数据库自动维护...")

	// 1. 清理过期数据
	m.cleanupExpiredData(ctx)

	// 2. 清理完成任务的旧指令（保留 30 天）
	m.cleanupOldCommands(ctx)

	// 3. 归档旧任务（保留 90 天）
	m.archiveOldTasks(ctx)

	// 4. VACUUM ANALYZE（优化表 + 更新统计信息）
	m.vacuumAnalyze(ctx)

	// 5. 重建膨胀索引
	m.reindexIfNeeded(ctx)

	log.Println("✅ 数据库自动维护完成")
}

// cleanupExpiredData 清理过期数据。
func (m *Maintainer) cleanupExpiredData(ctx context.Context) {
	// 清理过期未取的指令
	result, err := m.db.ExecContext(ctx, `
		DELETE FROM commands 
		WHERE status = 'pending' 
		AND expire_at < NOW() - INTERVAL '7 days'
	`)
	if err != nil {
		log.Printf("⚠️ 清理过期指令失败: %v", err)
		return
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("🗑️ 清理过期指令: %d 条", rows)
	}

	// 清理僵尸维护窗口（超过 24 小时）
	result, err = m.db.ExecContext(ctx, `
		UPDATE hosts 
		SET maintenance_until = NULL, maintenance_reason = '' 
		WHERE maintenance_until < NOW() - INTERVAL '1 day'
	`)
	if err != nil {
		log.Printf("⚠️ 清理维护窗口失败: %v", err)
		return
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("🗑️ 清理僵尸维护窗口: %d 台主机", rows)
	}
}

// cleanupOldCommands 清理旧指令（保留 30 天）。
func (m *Maintainer) cleanupOldCommands(ctx context.Context) {
	result, err := m.db.ExecContext(ctx, `
		DELETE FROM commands 
		WHERE status IN ('success', 'failed', 'rolled_back', 'timeout', 'expired', 'canceled') 
		AND finished_at < NOW() - INTERVAL '30 days'
	`)
	if err != nil {
		log.Printf("⚠️ 清理旧指令失败: %v", err)
		return
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("🗑️ 清理旧指令: %d 条", rows)
	}
}

// archiveOldTasks 归档旧任务（可选：移到归档表，此处简化为标记）。
func (m *Maintainer) archiveOldTasks(ctx context.Context) {
	// 标记 90 天前完成的任务（实际可移到 upgrade_tasks_archive 表）
	// 注意：upgrade_tasks 无 finished_at 字段，终态时间以 updated_at 为准。
	result, err := m.db.ExecContext(ctx, `
		UPDATE upgrade_tasks
		SET status = 'archived'
		WHERE status IN ('success', 'failed', 'canceled', 'rolled_back')
		  AND updated_at < NOW() - INTERVAL '90 days'
	`)
	if err != nil {
		log.Printf("⚠️ 归档旧任务失败: %v", err)
		return
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("📦 归档旧任务: %d 个", rows)
	}
}

// vacuumAnalyze 执行 VACUUM ANALYZE（优化 + 统计信息）。
func (m *Maintainer) vacuumAnalyze(ctx context.Context) {
	tables := []string{
		"hosts", "agents", "components", "artifacts",
		"upgrade_tasks", "task_hosts", "task_batches", "commands",
	}

	for _, table := range tables {
		_, err := m.db.ExecContext(ctx, "VACUUM ANALYZE "+table)
		if err != nil {
			log.Printf("⚠️ VACUUM ANALYZE %s 失败: %v", table, err)
		}
	}
	log.Println("✅ VACUUM ANALYZE 完成")
}

// reindexIfNeeded 重建膨胀索引（检测膨胀率 > 50%）。
func (m *Maintainer) reindexIfNeeded(ctx context.Context) {
	// 查询膨胀索引（简化：此处跳过复杂查询，可集成 pg_stat_user_indexes）
	// 生产环境可用 pgstattuple 扩展检测膨胀率
	log.Println("ℹ️ 索引重建检测（需 pgstattuple 扩展）")
}

// CleanupSyncLogs 清理同步日志（保留最近 100 条）。
func (m *Maintainer) CleanupSyncLogs(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, `
		DELETE FROM sync_logs 
		WHERE id NOT IN (
			SELECT id FROM sync_logs ORDER BY id DESC LIMIT 100
		)
	`)
	return err
}

// GetMaintenanceStatus 获取维护状态统计。
func (m *Maintainer) GetMaintenanceStatus(ctx context.Context) (map[string]any, error) {
	stats := map[string]any{}

	// 数据库大小
	var dbSize string
	_ = m.db.QueryRowContext(ctx, `SELECT pg_size_pretty(pg_database_size(current_database()))`).Scan(&dbSize)
	stats["db_size"] = dbSize

	// 表数据量
	tables := []string{"hosts", "agents", "components", "artifacts", "upgrade_tasks", "commands"}
	tableCounts := map[string]int{}
	for _, table := range tables {
		var count int
		_ = m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count)
		tableCounts[table] = count
	}
	stats["table_counts"] = tableCounts

	// 待清理数据
	var expiredCommands int
	_ = m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commands WHERE expire_at < NOW() - INTERVAL '7 days'`).Scan(&expiredCommands)
	stats["expired_commands"] = expiredCommands

	var oldCommands int
	_ = m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commands WHERE finished_at < NOW() - INTERVAL '30 days'`).Scan(&oldCommands)
	stats["old_commands"] = oldCommands

	return stats, nil
}
