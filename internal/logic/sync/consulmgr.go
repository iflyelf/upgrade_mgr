package sync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/hostfieldmap"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/consulmgr"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/pglock"
)

// consulMgrLockKey consul_mgr 同步跨实例互斥键（与直连 Consul 的锁分开，
// 两种来源可以并行同步，各自仍然互斥）。
const consulMgrLockKey int64 = 0x7570677261646505 // "upgrade\x05"

// MgrResult 一次 consul_mgr 同步的统计。
type MgrResult struct {
	Groups       int
	Hosts        int
	Upserted     int
	Mappings     int
	FailedGroups []string
	Partial      bool
	StartedAt    time.Time
	Duration     time.Duration
}

// SyncFromConsulMgr 从 consul_mgr 拉取主机清单并 upsert 到 hosts。
//
// 与 SyncFromConsul 的差异：
//   - 数据来自 consul_mgr 的服务间只读接口，可一次覆盖多套 Consul（多服务组）；
//   - 主机 source 标记为 consul_mgr，与直连 Consul 的主机区分；
//   - 不做「清理已移除主机」：consul_mgr 侧的服务组可能被临时禁用或连接失败，
//     据此删本地主机风险过高。清理仍只由直连 Consul 的同步负责。
//
// 字段映射同样完全由 host_field_mappings 驱动（source=consul_mgr），
// 服务组名/编码等结构信息作为基础值，可被映射规则覆盖。
func (s *Syncer) SyncFromConsulMgr(ctx context.Context, cli *consulmgr.Client, groupID int64) (*MgrResult, error) {
	if cli == nil {
		return nil, fmt.Errorf("consul_mgr 未配置：请在「设置 → consul_mgr 对接」填写地址与服务令牌")
	}
	lockCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	lock, ok, err := pglock.Try(lockCtx, s.db, consulMgrLockKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSyncInProgress
	}
	defer lock.Unlock()

	res := &MgrResult{StartedAt: time.Now()}

	mappings, err := hostfieldmap.NewRepo(s.db).Enabled(ctx, hostfieldmap.SourceConsulMgr)
	if err != nil {
		return nil, fmt.Errorf("读取主机字段映射失败: %w", err)
	}
	set := hostfieldmap.NewSet(mappings)
	res.Mappings = set.Len()

	remote, err := cli.FetchHosts(ctx, groupID)
	if err != nil {
		return nil, err
	}
	res.Groups = remote.Groups
	res.FailedGroups = remote.FailedGroups
	res.Partial = remote.Partial
	res.Hosts = len(remote.Hosts)

	now := time.Now()
	for _, rh := range remote.Hosts {
		if rh.IP == "" {
			continue
		}
		row := &hostRow{
			IP:         rh.IP,
			Node:       rh.Node,
			Datacenter: rh.Datacenter,
			Meta:       mergeMgrMeta(rh),
		}
		if err := s.upsert(ctx, row, set, hostfieldmap.SourceConsulMgr, now); err != nil {
			return res, err
		}
		res.Upserted++
	}

	res.Duration = time.Since(res.StartedAt)
	return res, nil
}

// mgrStructuralKeys consul_mgr 导出中「非 Meta」的结构化信息，
// 以保留键的形式并入 Meta，这样它们和普通 Meta 键一样可被映射规则使用。
//
// 加 __ 前缀是为了与业务方自定义的 ServiceMeta 键区分：
// Consul Meta 键不允许以下划线开头（sourceKeyRe 要求字母数字开头），
// 所以这些键只能由平台注入，业务方的 Meta 不会与之冲突。
// 页面「扫描来源键」会把它们一并列出，管理员可自行决定是否映射。
const (
	MgrKeyGroupName  = "__group_name"
	MgrKeyGroupCode  = "__group_code"
	MgrKeyServices   = "__services"
	MgrKeyTags       = "__tags"
	MgrKeyHealth     = "__health"
	MgrKeyNode       = "__node"
	MgrKeyDatacenter = "__datacenter"
)

// MgrStructuralKeys 返回平台注入的保留键及说明（供页面展示）。
func MgrStructuralKeys() map[string]string {
	return map[string]string{
		MgrKeyGroupName:  "consul_mgr 服务组名",
		MgrKeyGroupCode:  "consul_mgr 服务组编码",
		MgrKeyServices:   "该主机上的服务名（逗号分隔）",
		MgrKeyTags:       "服务标签（逗号分隔）",
		MgrKeyHealth:     "健康状态 passing/warning/critical",
		MgrKeyNode:       "Consul 节点名",
		MgrKeyDatacenter: "数据中心",
	}
}

// mergeMgrMeta 把 consul_mgr 的结构化字段与 ServiceMeta 合并成统一的来源键空间。
//
// 业务方的 Meta 优先：若 Meta 中已有同名键（理论上不会，保留键带 __ 前缀），
// 不被结构化值覆盖。
func mergeMgrMeta(h consulmgr.Host) map[string]string {
	out := make(map[string]string, len(h.Meta)+7)
	for k, v := range h.Meta {
		out[k] = v
	}
	put := func(k, v string) {
		if v == "" {
			return
		}
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	put(MgrKeyGroupName, h.GroupName)
	put(MgrKeyGroupCode, h.GroupCode)
	put(MgrKeyServices, strings.Join(h.Services, ","))
	put(MgrKeyTags, strings.Join(h.Tags, ","))
	put(MgrKeyHealth, h.Health)
	put(MgrKeyNode, h.Node)
	put(MgrKeyDatacenter, h.Datacenter)
	return out
}
