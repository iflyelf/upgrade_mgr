// Package sync 从 Consul 同步主机清单与业务分组到本地 hosts 表（可选依赖）。
//
// 设计：Consul 只是主机来源之一（source=consul）。同步为可开关 + 可定时；
// Consul 不可用时不影响平台其它功能（Agent 自注册与手工导入仍可用）。
package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/consul"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/pglock"
)

// ErrSyncInProgress 同一时刻已有其他实例/请求在执行 Consul 同步。
var ErrSyncInProgress = errors.New("已有 Consul 同步任务在执行")

// syncLockKey Consul 同步跨实例互斥键。
const syncLockKey int64 = 0x7570677261646504 // "upgrade\x04"

// Result 一次同步的统计。
type Result struct {
	Services       int
	Instances      int
	Upserted       int
	Deleted        int // 本次清理掉的、已从 Consul 移除的纯 Consul 主机数
	FailedServices int // 查询实例失败的服务数；>0 时跳过清理
	Partial        bool
	CleanupSkipped string // 非空表示未执行清理及原因
	StartedAt      time.Time
	Duration       time.Duration
}

// Syncer 主机同步器。
type Syncer struct {
	db *sql.DB
}

// NewSyncer 创建同步器。
func NewSyncer(db *sql.DB) *Syncer { return &Syncer{db: db} }

// SyncFromConsul 遍历 Consul 全部服务实例，按 IP 聚合并 upsert 到 hosts。
//
// 业务分组字段取自 ServiceMeta（account/app_code/group/department/vendor/os_type/host_type）。
// 同一 IP 可能出现在多个服务中，后出现的补齐空字段，不覆盖已有非空值。
//
// cleanupMissing=true 时，本次同步结束后会清理「上次同步时间早于本轮、且已不在
// Consul 中」的纯 Consul 主机。安全保护：
//   - 只删 source='consul' 且没有 agent_id 的主机；
//   - 存在 agents 记录、未完成任务/指令的主机不删；
//   - 任一服务实例查询失败（可能只拿到部分清单）时整轮跳过清理；
//   - 本轮未发现任何主机时跳过清理，避免 Consul 空/配置错误导致清空。
func (s *Syncer) SyncFromConsul(ctx context.Context, cli *consul.Client, cleanupMissing bool) (*Result, error) {
	if cli == nil {
		return nil, fmt.Errorf("consul 客户端未初始化")
	}
	// 多副本/手动与定时并发时只允许一个同步写库，避免重复查询与重复 upsert。
	lockCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	lock, ok, err := pglock.Try(lockCtx, s.db, syncLockKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSyncInProgress
	}
	defer lock.Unlock()

	res := &Result{StartedAt: time.Now()}

	services, err := cli.Services()
	if err != nil {
		return nil, err
	}
	res.Services = len(services)

	// 按 IP 聚合，避免同一主机多服务重复写库
	agg := map[string]*hostRow{}
	for name := range services {
		if name == "consul" {
			continue
		}
		insts, err := cli.ServiceInstances(name)
		if err != nil {
			// 单服务失败仍同步其它服务，但本轮清单不完整，必须跳过清理。
			res.FailedServices++
			res.Partial = true
			continue
		}
		res.Instances += len(insts)
		for _, in := range insts {
			if in.Address == "" {
				continue
			}
			h := agg[in.Address]
			if h == nil {
				h = &hostRow{IP: in.Address}
				agg[in.Address] = h
			}
			h.merge(in)
		}
	}

	now := time.Now()
	for _, h := range agg {
		if err := s.upsert(ctx, h, now); err != nil {
			return res, err
		}
		res.Upserted++
	}

	if !cleanupMissing {
		res.CleanupSkipped = "未开启自动清理"
	} else if res.Partial {
		res.CleanupSkipped = fmt.Sprintf("%d 个服务实例查询失败，已跳过清理", res.FailedServices)
	} else if len(agg) == 0 {
		res.CleanupSkipped = "本次未发现任何主机，已跳过清理以防误删"
	} else {
		deleted, err := s.cleanupMissingHosts(ctx, now)
		if err != nil {
			return res, fmt.Errorf("清理 Consul 已移除主机失败: %w", err)
		}
		res.Deleted = deleted
	}

	res.Duration = time.Since(res.StartedAt)
	return res, nil
}

type hostRow struct {
	IP         string
	Hostname   string
	Node       string
	Datacenter string
	OSType     string
	Account    string
	AppCode    string
	BizGroup   string
	Department string
	Vendor     string
	HostType   string
}

func (h *hostRow) merge(in consul.ServiceInstance) {
	setIfEmpty(&h.Node, in.Node)
	setIfEmpty(&h.Datacenter, in.Datacenter)
	m := in.Meta
	setIfEmpty(&h.OSType, m["os_type"])
	setIfEmpty(&h.Account, m["account"])
	setIfEmpty(&h.AppCode, m["app_code"])
	setIfEmpty(&h.BizGroup, m["group"])
	setIfEmpty(&h.Department, m["department"])
	setIfEmpty(&h.Vendor, m["vendor"])
	setIfEmpty(&h.HostType, m["host_type"])
	setIfEmpty(&h.Hostname, m["name"])
}

// upsert 写入/更新 hosts；仅补齐来自 Consul 的分组字段，不覆盖 Agent 上报的 os/arch。
func (s *Syncer) upsert(ctx context.Context, h *hostRow, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO hosts (ip, hostname, consul_node, datacenter, os_type,
		                   account, app_code, biz_group, department, vendor, host_type,
		                   source, last_synced_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'consul',$12,$12,$12)
		ON CONFLICT (ip) DO UPDATE SET
			consul_node    = COALESCE(NULLIF(EXCLUDED.consul_node,''), hosts.consul_node),
			datacenter     = COALESCE(NULLIF(EXCLUDED.datacenter,''),  hosts.datacenter),
			os_type        = COALESCE(NULLIF(EXCLUDED.os_type,''),     hosts.os_type),
			account        = COALESCE(NULLIF(EXCLUDED.account,''),     hosts.account),
			app_code       = COALESCE(NULLIF(EXCLUDED.app_code,''),    hosts.app_code),
			biz_group      = COALESCE(NULLIF(EXCLUDED.biz_group,''),   hosts.biz_group),
			department     = COALESCE(NULLIF(EXCLUDED.department,''),  hosts.department),
			vendor         = COALESCE(NULLIF(EXCLUDED.vendor,''),      hosts.vendor),
			host_type      = COALESCE(NULLIF(EXCLUDED.host_type,''),   hosts.host_type),
			hostname       = COALESCE(NULLIF(hosts.hostname,''),       EXCLUDED.hostname),
			last_synced_at = EXCLUDED.last_synced_at,
			updated_at     = EXCLUDED.updated_at
	`, h.IP, h.Hostname, h.Node, h.Datacenter, h.OSType,
		h.Account, h.AppCode, h.BizGroup, h.Department, h.Vendor, h.HostType, now)
	return err
}

func setIfEmpty(dst *string, v string) {
	if *dst == "" && v != "" {
		*dst = v
	}
}

// cleanupBatchSize 单次事务最多删除的主机数，避免 Consul 大批离线时
// 单个事务持锁过久、阻塞 Agent 上报与任务创建。
const cleanupBatchSize = 500

// maxCleanupBatches 单次同步最多清理的批次数。防御并发不断新增过期主机时
// 清理循环长时间占用同步锁；剩余主机在下一轮同步继续清理。
const maxCleanupBatches = 20

// missingHostCond 返回「可以从 Consul 清单安全删除」的过滤条件。
//
// 参数占位符由调用方传入（不同语句的参数编号不同）：
// 条件中两次引用同一 syncedAt 参数。
func missingHostCond(syncedAtParam string) string {
	return `
		h.source='consul'
		AND (h.agent_id IS NULL OR h.agent_id='')
		AND (h.last_synced_at IS NULL OR h.last_synced_at < ` + syncedAtParam + `)
		AND (h.last_seen IS NULL OR h.last_seen < ` + syncedAtParam + `)
		AND (h.maintenance_until IS NULL OR h.maintenance_until <= NOW())
		AND NOT EXISTS (SELECT 1 FROM agents a WHERE a.ip = h.ip)
		AND NOT EXISTS (SELECT 1 FROM agents a WHERE a.host_id = h.id)
		AND NOT EXISTS (SELECT 1 FROM task_hosts th
		                WHERE th.host_id=h.id AND th.status IN ('pending','running'))
		AND NOT EXISTS (SELECT 1 FROM commands c
		                WHERE c.host_id=h.id AND c.status IN ('pending','dispatched','accepted','running'))`
}

// cleanupMissingHosts 清理本轮未出现在 Consul 中的纯 Consul 主机。
//
// syncedAt 与本轮所有 upsert 使用的 last_synced_at 相同：本轮出现的主机
// last_synced_at=syncedAt，不会被删除；上一轮及更早的才小于 syncedAt。
// 有 Agent、Agent 记录、未完成任务/指令的主机一律保留。
//
// 并发安全：按「task_hosts -> commands -> hosts」的顺序逐台加锁后再删除，
// 与 Agent Report/UpdateStage、HostDelete 的锁顺序一致。若直接 DELETE hosts，
// 外键级联会先锁 hosts 再锁子表，和正在落终态的 Agent 上报形成反向锁序，
// 极端情况下会死锁。
//
// 单轮最多处理 maxCleanupBatches 批，避免极端情况下长期占用同步锁；
// 剩余主机在下一轮同步继续清理（不影响正确性）。
func (s *Syncer) cleanupMissingHosts(ctx context.Context, syncedAt time.Time) (int, error) {
	total := 0
	for round := 0; round < maxCleanupBatches; round++ {
		ids, err := s.findMissingHosts(ctx, syncedAt, cleanupBatchSize)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		n, err := s.deleteMissingHosts(ctx, syncedAt, ids)
		if err != nil {
			return total, err
		}
		total += n
		// 整批都在加锁后复查中被保留：本轮不再重试，避免死循环。
		if n == 0 || len(ids) < cleanupBatchSize {
			return total, nil
		}
	}
	return total, nil
}

// findMissingHosts 无锁挑选候选主机；真正删除前会在事务内复查。
func (s *Syncer) findMissingHosts(ctx context.Context, syncedAt time.Time, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.id FROM hosts h
		WHERE `+missingHostCond("$1")+`
		ORDER BY h.id
		LIMIT $2`, syncedAt, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// deleteMissingHosts 在一个事务内按统一锁序删除候选主机。
func (s *Syncer) deleteMissingHosts(ctx context.Context, syncedAt time.Time, ids []int64) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	deleted := 0
	for _, id := range ids {
		// 与 Report/UpdateStage/HostDelete 相同：先锁逐机记录，再锁指令，最后锁主机。
		for _, q := range []string{
			`SELECT 1 FROM task_hosts WHERE host_id=$1 FOR UPDATE`,
			`SELECT 1 FROM commands WHERE host_id=$1 FOR UPDATE`,
		} {
			rows, err := tx.QueryContext(ctx, q, id)
			if err != nil {
				return deleted, err
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return deleted, err
			}
		}

		// 加锁后复查：期间可能新增 Agent/任务/指令，或主机重新出现在 Consul 中。
		res, err := tx.ExecContext(ctx, `
			DELETE FROM hosts h
			WHERE h.id=$1 AND `+missingHostCond("$2"), id, syncedAt)
		if err != nil {
			return deleted, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			deleted += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return deleted, err
	}
	return deleted, nil
}
