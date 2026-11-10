package org

import (
	"sort"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/flyiam"
)

// DeptBuilder 从用户部门属性增量推导组织树。
//
// 与 FlyIAM 的 /api/departments/export 使用同一套规则：
//   - 读取 properties 中的 deptIdLv0/1/2、deptNameLv0/1/2；
//   - 缺少部门 ID 时用「父路径 + 名称」生成稳定键；
//   - 用户计入最深一级非空部门。
//
// 放在本系统内推导的好处：按 Casdoor 分页逐页拉取，每次 HTTP 请求都很短，
// 不会像 FlyIAM 全量导出那样超过反向代理超时（503 Request Timeout）。
type DeptBuilder struct {
	m map[string]*flyiam.Department
}

// NewDeptBuilder 创建组织树构建器。
func NewDeptBuilder() *DeptBuilder {
	return &DeptBuilder{m: map[string]*flyiam.Department{}}
}

// AddUser 累加一个用户的部门属性。
func (b *DeptBuilder) AddUser(props map[string]string) {
	if b == nil || len(props) == 0 {
		return
	}
	type levelProp struct {
		id, name string
		level    int
	}
	levels := []levelProp{
		{props["deptIdLv0"], props["deptNameLv0"], 0},
		{props["deptIdLv1"], props["deptNameLv1"], 1},
		{props["deptIdLv2"], props["deptNameLv2"], 2},
	}
	parentKey := ""
	leafKey := ""
	for _, l := range levels {
		name := strings.TrimSpace(l.name)
		key := strings.TrimSpace(l.id)
		if key == "" && name != "" {
			key = "name:" + parentKey + "/" + name
		}
		if key == "" {
			continue
		}
		if name == "" {
			name = key
		}
		node, ok := b.m[key]
		if !ok {
			fullPath := name
			if p, okParent := b.m[parentKey]; okParent && parentKey != "" {
				fullPath = p.FullPath + "/" + name
			}
			node = &flyiam.Department{
				DeptID: key, DeptName: name, ParentID: parentKey,
				Level: l.level, FullPath: fullPath,
			}
			b.m[key] = node
		}
		parentKey = key
		leafKey = key
	}
	if leafKey != "" {
		b.m[leafKey].UserCount++
	}
}

// List 返回按 fullPath 排序的组织节点。
func (b *DeptBuilder) List() []flyiam.Department {
	if b == nil {
		return nil
	}
	out := make([]flyiam.Department, 0, len(b.m))
	for _, d := range b.m {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FullPath == out[j].FullPath {
			return out[i].DeptID < out[j].DeptID
		}
		return out[i].FullPath < out[j].FullPath
	})
	return out
}
