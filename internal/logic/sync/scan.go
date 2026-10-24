package sync

import (
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/consul"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/consulmgr"
)

// ScanConsulMeta 遍历 Consul 全部服务实例，按 IP 聚合 ServiceMeta。
//
// 用于「扫描来源键」：页面据此列出外部实际存在哪些 Meta 键，
// 管理员勾选后再导入为字段定义与映射，不做任何自动创建。
//
// 聚合规则与 SyncFromConsul 完全一致（按 IP、首个非空值优先），
// 保证「扫描看到的值」就是「同步会写入的值」。
//
// 返回：每台主机的 Meta 快照、扫描到的主机数。
// 单个服务查询失败只跳过该服务（与同步一致），不中断整次扫描。
func ScanConsulMeta(cli *consul.Client) ([]map[string]string, int, error) {
	if cli == nil {
		return nil, 0, nil
	}
	services, err := cli.Services()
	if err != nil {
		return nil, 0, err
	}
	agg := map[string]*hostRow{}
	for name := range services {
		if name == "consul" {
			continue
		}
		insts, err := cli.ServiceInstances(name)
		if err != nil {
			continue
		}
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

	out := make([]map[string]string, 0, len(agg))
	for _, h := range agg {
		meta := h.Meta
		if meta == nil {
			meta = map[string]string{}
		}
		// Consul 结构属性也作为可映射的来源键暴露，与 consul_mgr 口径一致。
		if h.Node != "" {
			putIfAbsent(meta, MgrKeyNode, h.Node)
		}
		if h.Datacenter != "" {
			putIfAbsent(meta, MgrKeyDatacenter, h.Datacenter)
		}
		out = append(out, meta)
	}
	return out, len(agg), nil
}

// MgrHostMeta 返回 consul_mgr 主机的完整来源键空间（Meta + 平台注入的保留键）。
//
// 与 SyncFromConsulMgr 使用同一个合并函数，保证扫描与同步看到的键一致。
func MgrHostMeta(h consulmgr.Host) map[string]string { return mergeMgrMeta(h) }

func putIfAbsent(m map[string]string, k, v string) {
	if strings.TrimSpace(v) == "" {
		return
	}
	if _, ok := m[k]; !ok {
		m[k] = v
	}
}
