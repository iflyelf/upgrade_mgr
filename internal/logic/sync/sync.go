// Package sync 从 Consul 同步主机清单与业务分组到本地 hosts 表（可选依赖）。
//
// 设计：Consul 只是主机来源之一（source=consul）。同步为可开关 + 可定时；
// Consul 不可用时不影响平台其它功能（Agent 自注册与手工导入仍可用）。
package sync

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/consul"
)

// Result 一次同步的统计。
type Result struct {
	Services  int
	Instances int
	Upserted  int
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
// 业务分组字段取自 ServiceMeta（account/app_code/group/department/vendor/os_type/host_type）。
// 同一 IP 可能出现在多个服务中，后出现的补齐空字段，不覆盖已有非空值。
func (s *Syncer) SyncFromConsul(ctx context.Context, cli *consul.Client) (*Result, error) {
	if cli == nil {
		return nil, fmt.Errorf("consul 客户端未初始化")
	}
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
			continue // 单服务失败不影响整体
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
