package handler

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/logic/hostfieldmap"
	synclogic "github.com/iflyelf/upgrade_mgr/internal/logic/sync"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// ListHostFieldMappings 列出主机字段映射（管理员）。
//
// 同时返回可选的内置列与来源列表，页面无需硬编码下拉项。
func ListHostFieldMappings(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		source := strings.TrimSpace(r.URL.Query().Get("source"))
		list, err := hostfieldmap.NewRepo(s.DB).List(r.Context(), source)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{
			"mappings": list,
			"sources":  hostfieldmap.Sources(),
			"columns":  hostfieldmap.ColumnOptions(),
			// 平台注入的保留键（consul_mgr 的服务组名等），供页面提示可映射来源。
			"reserved_keys": synclogic.MgrStructuralKeys(),
		})
	}
}

// CreateHostFieldMapping 新增映射（管理员）。
func CreateHostFieldMapping(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m hostfieldmap.Mapping
		if err := httpx.DecodeJSON(r, &m); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		id, err := hostfieldmap.NewRepo(s.DB).Create(r.Context(), &m)
		auditOp(s, r, audit.ActionHostFieldMapCreate, "host_field_mapping",
			m.SourceKey, m.Source+":"+m.SourceKey+"→"+m.TargetKey, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, map[string]any{"id": id})
	}
}

// UpdateHostFieldMapping 更新映射（管理员）。
func UpdateHostFieldMapping(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "映射 ID 无效")
			return
		}
		var m hostfieldmap.Mapping
		if err := httpx.DecodeJSON(r, &m); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		m.ID = id
		err = hostfieldmap.NewRepo(s.DB).Update(r.Context(), &m)
		auditOp(s, r, audit.ActionHostFieldMapUpdate, "host_field_mapping",
			strconv.FormatInt(id, 10), m.Source+":"+m.SourceKey+"→"+m.TargetKey, err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// DeleteHostFieldMapping 删除映射（管理员）。
func DeleteHostFieldMapping(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			httpx.Fail(w, http.StatusBadRequest, "映射 ID 无效")
			return
		}
		err = hostfieldmap.NewRepo(s.DB).Delete(r.Context(), id)
		auditOp(s, r, audit.ActionHostFieldMapDelete, "host_field_mapping",
			strconv.FormatInt(id, 10), "", err)
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w, nil)
	}
}

// scannedKey 扫描到的一个来源键。
type scannedKey struct {
	Key string `json:"key"`
	// Count 出现该键的主机数；Samples 为最多 3 个样例值，便于判断字段含义。
	Count   int      `json:"count"`
	Samples []string `json:"samples"`
	// Mapped 是否已有映射规则；MappedTo 为目标描述。
	Mapped   bool   `json:"mapped"`
	MappedTo string `json:"mapped_to,omitempty"`
	// Reserved 为 true 表示平台注入的结构化键（非业务 ServiceMeta）。
	Reserved bool   `json:"reserved"`
	Note     string `json:"note,omitempty"`
}

// maxSamples 每个键最多保留的样例值数量。
const maxSamples = 3

// ScanHostFieldKeys 扫描外部来源的可用键（管理员）。
//
// 用途：主机字段映射需要知道「对方到底有哪些键」。这里真实拉一次数据，
// 统计每个键出现的主机数与样例值，页面据此勾选导入为字段定义 + 映射。
//
// 只读操作：不写任何库表，不创建字段，也不改主机。
func ScanHostFieldKeys(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		source := strings.TrimSpace(r.URL.Query().Get("source"))
		if source == "" {
			source = hostfieldmap.SourceConsul
		}

		var metas []map[string]string
		var scanned int
		switch source {
		case hostfieldmap.SourceConsul:
			cli := s.Consul()
			if cli == nil {
				httpx.Fail(w, http.StatusServiceUnavailable,
					"Consul 未启用或不可用，可在「设置 → Consul」配置后重试")
				return
			}
			m, n, err := synclogic.ScanConsulMeta(cli)
			if err != nil {
				httpx.FailErrf(w, http.StatusBadGateway, "读取 Consul 失败", err)
				return
			}
			metas, scanned = m, n
		case hostfieldmap.SourceConsulMgr:
			cli := s.ConsulMgr()
			if cli == nil {
				httpx.Fail(w, http.StatusServiceUnavailable,
					"未配置 consul_mgr 地址与服务令牌，请先到「设置 → consul_mgr 对接」填写")
				return
			}
			res, err := cli.FetchHosts(r.Context(), s.ConsulMgrGroupID())
			if err != nil {
				httpx.FailErrf(w, http.StatusBadGateway, "读取 consul_mgr 失败", err)
				return
			}
			for _, h := range res.Hosts {
				metas = append(metas, synclogic.MgrHostMeta(h))
			}
			scanned = len(res.Hosts)
		default:
			httpx.Fail(w, http.StatusBadRequest, "来源不支持（仅 consul / consul_mgr）")
			return
		}

		existing, err := hostfieldmap.NewRepo(s.DB).List(r.Context(), source)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		mapped := map[string]string{}
		for _, m := range existing {
			target := m.TargetKey
			if m.TargetKind == hostfieldmap.KindColumn {
				target = "内置列 " + m.TargetKey
			} else {
				target = "自定义字段 " + m.TargetKey
			}
			if !m.Enabled {
				target += "（已停用）"
			}
			mapped[m.SourceKey] = target
		}

		httpx.OK(w, map[string]any{
			"source": source, "hosts_scanned": scanned,
			"keys": aggregateKeys(metas, mapped),
		})
	}
}

// aggregateKeys 统计键出现次数与样例值，并标注是否已映射。
func aggregateKeys(metas []map[string]string, mapped map[string]string) []scannedKey {
	reserved := synclogic.MgrStructuralKeys()
	type acc struct {
		count   int
		samples []string
		seen    map[string]bool
	}
	stat := map[string]*acc{}
	for _, meta := range metas {
		for k, v := range meta {
			v = strings.TrimSpace(v)
			if k == "" || v == "" {
				continue
			}
			a := stat[k]
			if a == nil {
				a = &acc{seen: map[string]bool{}}
				stat[k] = a
			}
			a.count++
			if len(a.samples) < maxSamples && !a.seen[v] {
				a.seen[v] = true
				a.samples = append(a.samples, v)
			}
		}
	}

	out := make([]scannedKey, 0, len(stat))
	for k, a := range stat {
		target, ok := mapped[k]
		out = append(out, scannedKey{
			Key: k, Count: a.count, Samples: a.samples,
			Mapped: ok, MappedTo: target,
			Reserved: reserved[k] != "", Note: reserved[k],
		})
	}
	// 未映射的排前面（需要处理的在上），其次按出现次数降序、键名升序。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mapped != out[j].Mapped {
			return !out[i].Mapped
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}
