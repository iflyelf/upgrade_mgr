package hostfieldmap

import (
	"sort"
	"strings"
)

// Resolved 一台主机按映射解析出的待写入值。
type Resolved struct {
	// Columns 内置列 → 值。键一定在 AllowedColumns 内。
	Columns map[string]string
	// Custom 自定义字段键 → 值，写入 hosts.custom_fields。
	Custom map[string]string
	// OverwriteColumns / OverwriteCustom 标记哪些目标应强制覆盖已有值。
	OverwriteColumns map[string]bool
	OverwriteCustom  map[string]bool
}

// Set 组装器：按映射把外部 meta 解析为 Resolved。
//
// 多条映射指向同一目标时，按 sort_order 先到先得（不覆盖已解析到的非空值），
// 这样可以配置「优先取 app_code，没有就取 appCode」这类回退链。
type Set struct {
	// mappings 启用中的映射，已按 sort_order, id 排序。
	mappings []Mapping
}

// NewSet 构造解析器；只保留启用中的映射并按 sort_order 排序。
func NewSet(mappings []Mapping) *Set {
	out := make([]Mapping, 0, len(mappings))
	for _, m := range mappings {
		if !m.Enabled {
			continue
		}
		if m.TargetKind == KindColumn {
			// 再次校验白名单：即使库里被手工改坏，也不会写到非白名单列。
			if _, ok := AllowedColumns[m.TargetKey]; !ok {
				continue
			}
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].ID < out[j].ID
	})
	return &Set{mappings: out}
}

// Len 返回启用的映射条数。
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.mappings)
}

// SourceKeys 返回本组映射关心的外部键（去重，用于提示未映射的键）。
func (s *Set) SourceKeys() map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	for _, m := range s.mappings {
		out[m.SourceKey] = true
	}
	return out
}

// Resolve 按映射把外部 meta 解析成待写入值。
//
// meta 的键大小写敏感匹配；值为空白时视为缺失（不写入，也不触发覆盖）。
func (s *Set) Resolve(meta map[string]string) Resolved {
	res := Resolved{
		Columns:          map[string]string{},
		Custom:           map[string]string{},
		OverwriteColumns: map[string]bool{},
		OverwriteCustom:  map[string]bool{},
	}
	if s == nil || len(meta) == 0 {
		return res
	}
	for _, m := range s.mappings {
		val := strings.TrimSpace(meta[m.SourceKey])
		if val == "" {
			continue
		}
		switch m.TargetKind {
		case KindColumn:
			// 先到先得：允许用多条映射配置回退链。
			if _, exists := res.Columns[m.TargetKey]; exists {
				continue
			}
			res.Columns[m.TargetKey] = val
			if m.Overwrite {
				res.OverwriteColumns[m.TargetKey] = true
			}
		case KindCustom:
			if _, exists := res.Custom[m.TargetKey]; exists {
				continue
			}
			res.Custom[m.TargetKey] = val
			if m.Overwrite {
				res.OverwriteCustom[m.TargetKey] = true
			}
		}
	}
	return res
}

// Empty 判断解析结果是否没有任何可写入的值。
func (r Resolved) Empty() bool { return len(r.Columns) == 0 && len(r.Custom) == 0 }
