// Package metrics Prometheus 指标收集与暴露。
package metrics

import (
	"context"
	"database/sql"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Agent 指标
	AgentsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upgrade_mgr_agents_total",
		Help: "Total number of agents by status",
	}, []string{"status"})

	AgentsHeartbeatLag = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "upgrade_mgr_agents_heartbeat_lag_seconds",
		Help:    "Agent heartbeat lag in seconds",
		Buckets: []float64{30, 60, 120, 300, 600, 1800},
	})

	// AgentsVersionInfo Agent 版本/OS/架构分布，便于观察升级进度与遗留系统。
	AgentsVersionInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upgrade_mgr_agents_version_info",
		Help: "Number of agents by version/os/arch",
	}, []string{"version", "os", "arch"})

	// 任务指标
	TasksTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upgrade_mgr_tasks_total",
		Help: "Total number of tasks by status",
	}, []string{"status"})

	TaskHostsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upgrade_mgr_task_hosts_total",
		Help: "Total number of task hosts by status",
	}, []string{"task_id", "status"})

	TaskFailureRate = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upgrade_mgr_task_failure_rate",
		Help: "Task failure rate (0-100)",
	}, []string{"task_id"})

	TaskDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "upgrade_mgr_task_duration_seconds",
		Help:    "Task execution duration",
		Buckets: []float64{60, 300, 600, 1800, 3600, 7200},
	}, []string{"task_id", "status"})

	// 指令指标
	CommandsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "upgrade_mgr_commands_total",
		Help: "Total number of commands dispatched",
	}, []string{"type", "status"})

	CommandExecutionTime = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "upgrade_mgr_command_execution_seconds",
		Help:    "Command execution time",
		Buckets: []float64{10, 30, 60, 120, 300, 600},
	}, []string{"type"})

	// 制品指标
	ArtifactDownloads = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "upgrade_mgr_artifact_downloads_total",
		Help: "Total artifact downloads",
	}, []string{"component_id", "version", "arch"})

	ArtifactDownloadBytes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "upgrade_mgr_artifact_download_bytes_total",
		Help: "Total bytes downloaded",
	})

	// 外部文件代缓存指标：观察公网/内网依赖稳定性。
	//
	//   result=local      命中管理员上传的离线文件（未访问上游）
	//   result=local_miss local=1 探测未命中（老系统快速失败）
	//   result=cache_hit 命中本地缓存（未访问上游）
	//   result=fresh     未命中缓存，本次成功下载
	//   result=error     下载失败（上游不可达 / 校验失败 / 被 SSRF 策略拒绝）
	//   result=refresh   管理员强制刷新（成功或失败由 error 计数体现）
	ExternalBinaryRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "upgrade_mgr_external_binary_requests_total",
		Help: "External binary proxy requests by asset and result",
	}, []string{"name", "result"})

	ExternalBinaryBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "upgrade_mgr_external_binary_bytes_total",
		Help: "Bytes served by external binary proxy",
	}, []string{"name"})

	ExternalBinaryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "upgrade_mgr_external_binary_download_seconds",
		Help:    "Upstream download duration for external binaries",
		Buckets: []float64{0.1, 0.5, 1, 3, 10, 30, 60, 180},
	}, []string{"name", "result"})

	ExternalBinaryCacheAge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upgrade_mgr_external_binary_cache_age_seconds",
		Help: "Age of cached external binary (0 = not cached)",
	}, []string{"name"})

	// 外部文件可用性指标：定时探测每个白名单条目，便于配置告警。
	//
	//   upgrade_mgr_external_binary_up{name,type}         1=可用 0=不可用
	//   upgrade_mgr_external_binary_probe_seconds{name}   探测耗时
	//   upgrade_mgr_external_binary_cached{name}          1=有缓存 0=无
	//   upgrade_mgr_external_binary_cached_bytes{name}    缓存大小
	//   upgrade_mgr_external_binary_probe_total{name,result} 探测次数（success/error）
	ExternalBinaryUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upgrade_mgr_external_binary_up",
		Help: "Whether the external binary source is reachable (1=up, 0=down)",
	}, []string{"name", "type"})

	ExternalBinaryProbeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upgrade_mgr_external_binary_probe_seconds",
		Help: "Last probe duration in seconds",
	}, []string{"name"})

	ExternalBinaryCached = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upgrade_mgr_external_binary_cached",
		Help: "Whether a cached copy exists (1=yes, 0=no)",
	}, []string{"name"})

	ExternalBinaryCachedBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upgrade_mgr_external_binary_cached_bytes",
		Help: "Cached file size in bytes",
	}, []string{"name"})

	ExternalBinaryProbeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "upgrade_mgr_external_binary_probe_total",
		Help: "External binary availability probes by result",
	}, []string{"name", "result"})
)

// Collector 指标采集器。
type Collector struct {
	db   *sql.DB
	mu   sync.Mutex
	stop chan struct{}
}

// NewCollector 创建采集器。
func NewCollector(db *sql.DB) *Collector {
	return &Collector{db: db, stop: make(chan struct{})}
}

// Start 启动定期采集（每 30s）。
func (c *Collector) Start(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// 首次立即采集
	c.Collect()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case <-ticker.C:
			c.Collect()
		}
	}
}

// Stop 停止采集。
func (c *Collector) Stop() {
	close(c.stop)
}

// Collect 采集一次指标。
func (c *Collector) Collect() {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx := context.Background()

	// 清理已结束任务的 gauge（防止标签集无限增长）
	TaskHostsTotal.Reset()
	TaskFailureRate.Reset()
	TasksTotal.Reset()
	AgentsTotal.Reset()
	AgentsVersionInfo.Reset()

	c.collectAgents(ctx)
	c.collectTasks(ctx)
}

func (c *Collector) collectAgents(ctx context.Context) {
	// Agent 按状态统计
	rows, err := c.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM agents GROUP BY status
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	counts := map[string]float64{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err == nil {
			counts[status] = float64(count)
		}
	}
	if err := rows.Err(); err != nil {
		return
	}
	for status, count := range counts {
		AgentsTotal.WithLabelValues(status).Set(count)
	}

	// 心跳延迟（在线 Agent）
	rows, err = c.db.QueryContext(ctx, `
		SELECT EXTRACT(EPOCH FROM (NOW() - last_heartbeat_at))
		FROM agents WHERE status='online' AND last_heartbeat_at IS NOT NULL
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var lag float64
		if err := rows.Scan(&lag); err == nil {
			AgentsHeartbeatLag.Observe(lag)
		}
	}
	if err := rows.Err(); err != nil {
		return
	}
}

func (c *Collector) collectTasks(ctx context.Context) {
	// 任务按状态统计
	rows, err := c.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM upgrade_tasks GROUP BY status
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err == nil {
			TasksTotal.WithLabelValues(status).Set(float64(count))
		}
	}
	if err := rows.Err(); err != nil {
		return
	}

	// 运行中任务的主机状态与失败率
	rows, err = c.db.QueryContext(ctx, `
		SELECT t.id, th.status, COUNT(*)
		FROM upgrade_tasks t
		JOIN task_hosts th ON th.task_id = t.id
		WHERE t.status IN ('running', 'paused')
		GROUP BY t.id, th.status
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	taskStats := map[int64]map[string]int{}
	for rows.Next() {
		var tid int64
		var status string
		var count int
		if err := rows.Scan(&tid, &status, &count); err == nil {
			if taskStats[tid] == nil {
				taskStats[tid] = map[string]int{}
			}
			taskStats[tid][status] = count
		}
	}
	if err := rows.Err(); err != nil {
		return
	}

	for tid, stats := range taskStats {
		tidStr := prometheus.Labels{"task_id": strconv.FormatInt(tid, 10)}
		for status, count := range stats {
			TaskHostsTotal.With(prometheus.Labels{"task_id": strconv.FormatInt(tid, 10), "status": status}).Set(float64(count))
		}

		total := 0
		failed := 0
		for s, c := range stats {
			total += c
			if s == "failed" || s == "timeout" {
				failed += c
			}
		}
		if total > 0 {
			failRate := float64(failed) / float64(total) * 100
			TaskFailureRate.With(tidStr).Set(failRate)
		}
	}
}
