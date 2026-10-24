// Package hostfieldmap 管理「外部来源键 → 本地主机字段」的映射。
//
// 为什么要有这个包：
//
//	同步主机时，外部来源（Consul ServiceMeta、consul_mgr）的键名由业务方决定，
//	各环境不一致。把映射写死在代码里意味着「加一个 rack 字段就要改代码发版」，
//	而且自定义字段（host_field_defs）永远拿不到外部数据。
//
//	本包把映射关系存库（host_field_mappings），页面可增删改：
//	  - target_kind=column：写入 hosts 的内置列（白名单内）；
//	  - target_kind=custom：写入 hosts.custom_fields，键须已在 host_field_defs 定义。
//
// 安全：列名绝不拼接外部输入。Apply 只会把值写入 AllowedColumns 中的列，
// 未在白名单内的目标一律跳过，杜绝 SQL 注入与越权写 ip/agent_id/source。
package hostfieldmap

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 来源标识。
const (
	SourceConsul    = "consul"
	SourceConsulMgr = "consul_mgr"
)

// 目标类型。
const (
	KindColumn = "column"
	KindCustom = "custom"
)

// AllowedColumns 可被映射写入的 hosts 内置列白名单。
//
// 只包含业务分组/描述类列。刻意排除：
//   - ip / agent_id / source / tenant_id：主机身份与归属，由平台控制；
//   - os_family / os_version / arch / kernel：Agent 实测值，外部不得覆盖；
//   - agent_* / last_* / maintenance_*：运行状态，由心跳与任务维护。
//
// 与迁移 029 的 CHECK 约束保持一致，两处同时把关。
var AllowedColumns = map[string]string{
	"hostname":    "主机名",
	"consul_node": "Consul 节点",
	"datacenter":  "数据中心",
	"os_type":     "系统类型",
	"account":     "账号",
	"app_code":    "应用编码",
	"biz_group":   "业务组",
	"department":  "部门",
	"vendor":      "厂商",
	"host_type":   "主机类型",
}

// Sources 支持的来源（供页面渲染下拉）。
func Sources() []string { return []string{SourceConsul, SourceConsulMgr} }

// ColumnOptions 返回白名单列及中文名，按列名排序（页面下拉用）。
func ColumnOptions() []Option {
	out := make([]Option, 0, len(AllowedColumns))
	for k, label := range AllowedColumns {
		out = append(out, Option{Key: k, Label: label})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Option 页面下拉选项。
type Option struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Mapping 一条映射规则。
type Mapping struct {
	ID         int64     `json:"id"`
	Source     string    `json:"source"`
	SourceKey  string    `json:"sourceKey"`
	TargetKind string    `json:"targetKind"`
	TargetKey  string    `json:"targetKey"`
	Enabled    bool      `json:"enabled"`
	Overwrite  bool      `json:"overwrite"`
	SortOrder  int       `json:"sortOrder"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Repo 映射仓储。
type Repo struct{ db *sql.DB }

// NewRepo 创建仓储。
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

// sourceKeyRe 外部来源键：Consul Meta 键允许字母数字与 - . _
//
// 同时允许 __ 开头的保留键：平台会把 consul_mgr 的结构化信息
// （服务组名/服务名/健康状态等，见 logic/sync 的 MgrStructuralKeys）
// 以 __ 前缀注入来源键空间，让它们和普通 Meta 键一样可被映射。
// 业务方的 Consul Meta 键不会以下划线开头，不存在冲突。
var sourceKeyRe = regexp.MustCompile(`^(__)?[A-Za-z0-9][A-Za-z0-9_.\-]{0,127}$`)

// customKeyRe 自定义字段键：与 fielddef 的规则一致。
var customKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// validate 校验并规范化一条映射。
//
// customKeys 为当前已定义的自定义字段键集合；为 nil 时跳过该校验
// （用于不需要联动校验的场景，如仅做格式检查）。
func validate(m *Mapping, customKeys map[string]bool) error {
	m.Source = strings.ToLower(strings.TrimSpace(m.Source))
	m.SourceKey = strings.TrimSpace(m.SourceKey)
	m.TargetKind = strings.ToLower(strings.TrimSpace(m.TargetKind))
	m.TargetKey = strings.TrimSpace(m.TargetKey)

	if m.Source == "" {
		m.Source = SourceConsul
	}
	if m.Source != SourceConsul && m.Source != SourceConsulMgr {
		return fmt.Errorf("来源 %q 不支持（仅 consul / consul_mgr）", m.Source)
	}
	if !sourceKeyRe.MatchString(m.SourceKey) {
		return fmt.Errorf("来源键 %q 非法（字母数字开头，可含 - . _，最多 128 字符）", m.SourceKey)
	}
	switch m.TargetKind {
	case KindColumn:
		if _, ok := AllowedColumns[m.TargetKey]; !ok {
			return fmt.Errorf("内置列 %q 不在允许范围内（可用：%s）",
				m.TargetKey, strings.Join(allowedColumnNames(), ", "))
		}
	case KindCustom:
		if !customKeyRe.MatchString(m.TargetKey) {
			return fmt.Errorf("自定义字段键 %q 非法（字母开头，仅字母数字下划线）", m.TargetKey)
		}
		if customKeys != nil && !customKeys[m.TargetKey] {
			return fmt.Errorf("自定义字段 %q 尚未定义，请先在「人员组织 → 主机字段」中创建", m.TargetKey)
		}
	default:
		return fmt.Errorf("目标类型 %q 不支持（仅 column / custom）", m.TargetKind)
	}
	return nil
}

func allowedColumnNames() []string {
	out := make([]string, 0, len(AllowedColumns))
	for k := range AllowedColumns {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const selectCols = `id, source, source_key, target_kind, target_key,
	enabled, overwrite, sort_order, created_at, updated_at`

// List 列出映射；source 为空时返回全部。
func (r *Repo) List(ctx context.Context, source string) ([]Mapping, error) {
	query := `SELECT ` + selectCols + ` FROM host_field_mappings`
	args := []any{}
	if s := strings.TrimSpace(source); s != "" {
		query += ` WHERE source = $1`
		args = append(args, s)
	}
	query += ` ORDER BY source, sort_order, id`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mapping{}
	for rows.Next() {
		var m Mapping
		if err := rows.Scan(&m.ID, &m.Source, &m.SourceKey, &m.TargetKind, &m.TargetKey,
			&m.Enabled, &m.Overwrite, &m.SortOrder, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Enabled 返回某来源启用中的映射（同步时使用）。
func (r *Repo) Enabled(ctx context.Context, source string) ([]Mapping, error) {
	all, err := r.List(ctx, source)
	if err != nil {
		return nil, err
	}
	out := make([]Mapping, 0, len(all))
	for _, m := range all {
		if m.Enabled {
			out = append(out, m)
		}
	}
	return out, nil
}

// customFieldKeys 读取已定义的主机自定义字段键。
func (r *Repo) customFieldKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT field_key FROM host_field_defs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// Create 新增映射。
func (r *Repo) Create(ctx context.Context, m *Mapping) (int64, error) {
	keys, err := r.customFieldKeys(ctx)
	if err != nil {
		return 0, err
	}
	if err := validate(m, keys); err != nil {
		return 0, err
	}
	var id int64
	err = r.db.QueryRowContext(ctx, `
		INSERT INTO host_field_mappings
			(source, source_key, target_kind, target_key, enabled, overwrite, sort_order)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		m.Source, m.SourceKey, m.TargetKind, m.TargetKey,
		m.Enabled, m.Overwrite, m.SortOrder).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("来源 %s 的键「%s」已有映射", m.Source, m.SourceKey)
		}
		return 0, err
	}
	return id, nil
}

// Update 更新映射。
func (r *Repo) Update(ctx context.Context, m *Mapping) error {
	keys, err := r.customFieldKeys(ctx)
	if err != nil {
		return err
	}
	if err := validate(m, keys); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE host_field_mappings SET
			source=$1, source_key=$2, target_kind=$3, target_key=$4,
			enabled=$5, overwrite=$6, sort_order=$7, updated_at=NOW()
		WHERE id=$8`,
		m.Source, m.SourceKey, m.TargetKind, m.TargetKey,
		m.Enabled, m.Overwrite, m.SortOrder, m.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("来源 %s 的键「%s」已有映射", m.Source, m.SourceKey)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("映射不存在")
	}
	return nil
}

// Delete 删除映射。
func (r *Repo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM host_field_mappings WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("映射不存在")
	}
	return nil
}

// isUniqueViolation 判断是否唯一约束冲突（与 fielddef 包保持一致）。
func isUniqueViolation(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "duplicate key") ||
		strings.Contains(err.Error(), "23505"))
}
