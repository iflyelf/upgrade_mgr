// Package sync 从 Consul 同步主机清单与业务分组到本地 hosts 表（可选依赖）。
//
// 设计：Consul 只是主机来源之一（source=consul）。同步为可开关 + 可定时；
// Consul 不可用时不影响平台其它功能（Agent 自注册与手工导入仍可用）。
package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/hostfieldmap"
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
	// Mappings 本次生效的字段映射条数；为 0 表示只写入主机身份与位置信息。
	Mappings  int
	StartedAt time.Time
	Duration  time.Duration
}

// Syncer 主机同步器。
type Syncer struct {
	db *sql.DB
}

// NewSyncer 创建同步器。
func NewSyncer(db *sql.DB) *Syncer { return &Syncer{db: db} }

// SyncFromConsul 遍历 Consul 全部服务实例，按 IP 聚合并 upsert 到 hosts。
//
// 字段映射完全由 host_field_mappings 表驱动（见 internal/logic/hostfieldmap）：
// 哪个 ServiceMeta 键写到哪个内置列或自定义字段，都在「人员组织 → 主机字段映射」
// 中配置，新增来源键不需要改代码。Node/Datacenter 是 Consul 的结构属性，
// 作为基础值直接写入，映射规则可再覆盖。
//
// 同一 IP 可能出现在多个服务中，Meta 取并集（首个非空值优先）。
//
// cleanupMissing=true 时，本次同步结束后会清理「上次同步时间早于本轮、且已不在
// Consul 中」的纯 Consul 主机。安全保护：
//   - 只删 source='consul' 且没有 agent_id 的主机；
//   - 存在 agents 记录、未完成任务/指令的主机不删；
//   - 任一服务实例查询失败（可能只拿到部分清单）时整轮跳过清理；
//   - 本轮未发现任何主机时跳过清理，避免 Consul 空/配置错误导致清空。
func (s *Syncer) SyncFromConsul(ctx context.Context, cli *consul.Client, cleanupMissing bool, concurrency int) (*Result, error) {
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

	// 字段映射来自数据库：同步过程中保持同一份快照，避免中途被改导致前后不一致。
	mappings, err := hostfieldmap.NewRepo(s.db).Enabled(ctx, hostfieldmap.SourceConsul)
	if err != nil {
		return nil, fmt.Errorf("读取主机字段映射失败: %w", err)
	}
	set := hostfieldmap.NewSet(mappings)
	res.Mappings = set.Len()

	services, err := cli.Services()
	if err != nil {
		return nil, err
	}
	res.Services = len(services)

	// 按 IP 聚合，避免同一主机多服务重复写库。
	// 服务实例查询使用配置的有界并发，降低大量服务时的总网络往返耗时。
	agg := map[string]*hostRow{}
	serviceNames := make([]string, 0, len(services))
	for name := range services {
		if name == "consul" {
			continue
		}
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	for _, fetched := range fetchServicesBounded(ctx, serviceNames, concurrency, cli.ServiceInstances) {
		if fetched.err != nil {
			// 单服务失败仍同步其它服务，但本轮清单不完整，必须跳过清理。
			res.FailedServices++
			res.Partial = true
			continue
		}
		res.Instances += len(fetched.instances)
		for _, in := range fetched.instances {
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
		if err := s.upsert(ctx, h, set, hostfieldmap.SourceConsul, now); err != nil {
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

// hostRow 一台主机的聚合结果。
//
// Meta 保存该 IP 上所有服务实例 ServiceMeta 的并集，不在此做任何键的取舍；
// 真正写哪些字段由 hostfieldmap 的映射规则在 upsert 时决定。
type hostRow struct {
	IP         string
	Node       string
	Datacenter string
	Meta       map[string]string
}

func (h *hostRow) merge(in consul.ServiceInstance) {
	setIfEmpty(&h.Node, in.Node)
	setIfEmpty(&h.Datacenter, in.Datacenter)
	if h.Meta == nil {
		h.Meta = map[string]string{}
	}
	// 首个非空值优先：同一 IP 的多个服务给出不同值时结果稳定。
	for k, v := range in.Meta {
		k = strings.TrimSpace(k)
		if k == "" || strings.TrimSpace(v) == "" {
			continue
		}
		if _, ok := h.Meta[k]; !ok {
			h.Meta[k] = strings.TrimSpace(v)
		}
	}
}

// upsert 写入/更新 hosts。
//
// 写入哪些字段完全由映射规则决定（hostfieldmap），不含任何硬编码的 Meta 键：
//   - consul_node / datacenter 取自 Consul 记录自身的位置信息（非 Meta），
//     若映射显式指向这两列则以映射结果为准；
//   - 其余内置列与自定义字段按映射写入；
//   - overwrite=false（默认）只补空值，不覆盖人工维护过的内容；
//     overwrite=true 则以外部来源为准。
//
// 安全：列名只来自 hostfieldmap.AllowedColumns 白名单，不拼接任何外部输入；
// 值一律走占位符参数。
// source 只接受 hostfieldmap 定义的来源常量（consul / consul_mgr），
// 由调用方传入常量而非外部输入，且作为参数绑定、不拼接进 SQL。
func (s *Syncer) upsert(ctx context.Context, h *hostRow, set *hostfieldmap.Set,
	source string, now time.Time) error {
	resolved := set.Resolve(h.Meta)

	// Consul 记录自身的位置信息作为基础值，映射结果可覆盖。
	cols := map[string]string{}
	if h.Node != "" {
		cols["consul_node"] = h.Node
	}
	if h.Datacenter != "" {
		cols["datacenter"] = h.Datacenter
	}
	for k, v := range resolved.Columns {
		cols[k] = v
	}

	// 固定列顺序，保证 SQL 可复用（便于数据库缓存执行计划）与测试可预期。
	names := make([]string, 0, len(cols))
	for k := range cols {
		names = append(names, k)
	}
	sort.Strings(names)

	insertCols := []string{"ip", "source", "last_synced_at", "created_at", "updated_at"}
	args := []any{h.IP, source, now}
	// $1=ip, $2=source, $3=now（created_at/updated_at/last_synced_at 共用）
	placeholders := []string{"$1", "$2", "$3", "$3", "$3"}

	updates := []string{
		"last_synced_at = EXCLUDED.last_synced_at",
		"updated_at     = EXCLUDED.updated_at",
	}
	for _, name := range names {
		// 白名单复核：绝不把未授权的列名拼进 SQL。
		if _, ok := hostfieldmap.AllowedColumns[name]; !ok {
			continue
		}
		args = append(args, cols[name])
		insertCols = append(insertCols, name)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		if resolved.OverwriteColumns[name] {
			// 以外部来源为准：只要本次有非空值就覆盖。
			updates = append(updates, fmt.Sprintf(
				"%s = COALESCE(NULLIF(EXCLUDED.%s,''), hosts.%s)", name, name, name))
		} else {
			// 只补空值：本地已有内容时保留。
			updates = append(updates, fmt.Sprintf(
				"%s = COALESCE(NULLIF(hosts.%s,''), EXCLUDED.%s)", name, name, name))
		}
	}

	query := `INSERT INTO hosts (` + strings.Join(insertCols, ", ") + `)
		VALUES (` + strings.Join(placeholders, ",") + `)
		ON CONFLICT (ip) DO UPDATE SET ` + strings.Join(updates, ", ")
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return err
	}

	if len(resolved.Custom) == 0 {
		return nil
	}
	return s.upsertCustomFields(ctx, h.IP, resolved)
}

// upsertCustomFields 把映射到自定义字段的值合并进 hosts.custom_fields。
//
// overwrite=false 的键使用「仅当缺失或为空时写入」语义，不覆盖人工填写的值；
// overwrite=true 的键直接覆盖。两类键分开两次更新，SQL 更直白。
func (s *Syncer) upsertCustomFields(ctx context.Context, ip string, resolved hostfieldmap.Resolved) error {
	fill := map[string]string{}
	force := map[string]string{}
	for k, v := range resolved.Custom {
		if resolved.OverwriteCustom[k] {
			force[k] = v
		} else {
			fill[k] = v
		}
	}

	if len(force) > 0 {
		data, err := json.Marshal(force)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `
			UPDATE hosts
			SET custom_fields = COALESCE(custom_fields,'{}'::jsonb) || $2::jsonb,
			    updated_at = NOW()
			WHERE ip = $1`, ip, string(data)); err != nil {
			return err
		}
	}
	if len(fill) > 0 {
		data, err := json.Marshal(fill)
		if err != nil {
			return err
		}
		// 外部值作为基础，本地非空值覆盖其上：等价于「只填空缺」。
		// 先剔除空串再合并，让历史遗留的空值也能被外部值补齐。
		if _, err := s.db.ExecContext(ctx, `
			UPDATE hosts
			SET custom_fields = $2::jsonb || (
			        SELECT COALESCE(jsonb_object_agg(k, v), '{}'::jsonb)
			        FROM jsonb_each_text(COALESCE(custom_fields,'{}'::jsonb)) AS e(k, v)
			        WHERE btrim(v) <> ''
			    ),
			    updated_at = NOW()
			WHERE ip = $1`, ip, string(data)); err != nil {
			return err
		}
	}
	return nil
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
