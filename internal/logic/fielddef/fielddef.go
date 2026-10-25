// Package fielddef 通用「字段定义」管理：用户扩展字段与主机扩展字段共用。
//
// 设计要点（与 FlyIAM / consul_mgr 对齐）：
//   - 字段定义存库（user_field_defs / host_field_defs），页面可增删改，零硬编码；
//   - 值单独存放：用户字段存 user_field_values，主机字段存 hosts.custom_fields(JSONB)；
//   - 类型支持 text / textarea / number / select / date；
//   - 内置字段不可删除、不可改键，保证与 FlyIAM 同步的键稳定。
package fielddef

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// 字段类型。
const (
	TypeText     = "text"
	TypeTextarea = "textarea"
	TypeNumber   = "number"
	TypeSelect   = "select"
	TypeDate     = "date"
)

// Def 字段定义。
type Def struct {
	ID         int64     `json:"id"`
	FieldKey   string    `json:"fieldKey"`
	Label      string    `json:"label"`
	FieldType  string    `json:"fieldType"`
	Options    string    `json:"options"`
	ShowInList bool      `json:"showInList"`
	ShowInForm bool      `json:"showInForm"`
	Editable   bool      `json:"editable"`
	Builtin    bool      `json:"builtin"`
	SortOrder  int       `json:"sortOrder"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Kinds 支持的字段表。
//
// 表名与列名来自白名单常量，杜绝 SQL 注入（不接受外部传入表名）。
const (
	KindUser = "user"
	KindHost = "host"
)

type tableSpec struct {
	defTable  string
	valueCol  string // 值表/JSONB 用的键
	canDelete string
}

var tables = map[string]tableSpec{
	KindUser: {defTable: "user_field_defs"},
	KindHost: {defTable: "host_field_defs"},
}

// Tables 返回支持的字段表名（供 handler 校验参数）。
func Tables() []string { return []string{KindUser, KindHost} }

// repo 字段定义仓储（按 kind 绑定 SQL 表）。
type Repo struct {
	db   *sql.DB
	kind string
}

// NewRepo 创建仓储；kind 必须是 user / host。
func NewRepo(db *sql.DB, kind string) (*Repo, error) {
	if _, ok := tables[kind]; !ok {
		return nil, fmt.Errorf("不支持的字段类型 %q（仅 user/host）", kind)
	}
	return &Repo{db: db, kind: kind}, nil
}

func (r *Repo) table() string { return tables[r.kind].defTable }

var keyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// validKey 字段键：字母开头，仅字母数字下划线（可直接作为 JSON 键）。
func validKey(s string) bool { return keyRe.MatchString(s) }

// List 列出全部字段定义（按 sort_order, id 升序）。
func (r *Repo) List(ctx context.Context) ([]Def, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, field_key, label, field_type, COALESCE(options,''),
		       show_in_list, show_in_form, editable, builtin, sort_order, created_at, updated_at
		FROM `+r.table()+` ORDER BY sort_order ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Def{}
	for rows.Next() {
		var d Def
		if err := rows.Scan(&d.ID, &d.FieldKey, &d.Label, &d.FieldType, &d.Options,
			&d.ShowInList, &d.ShowInForm, &d.Editable, &d.Builtin, &d.SortOrder,
			&d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Get 读取单个定义。
func (r *Repo) Get(ctx context.Context, id int64) (*Def, error) {
	var d Def
	err := r.db.QueryRowContext(ctx, `
		SELECT id, field_key, label, field_type, COALESCE(options,''),
		       show_in_list, show_in_form, editable, builtin, sort_order, created_at, updated_at
		FROM `+r.table()+` WHERE id=$1`, id).
		Scan(&d.ID, &d.FieldKey, &d.Label, &d.FieldType, &d.Options,
			&d.ShowInList, &d.ShowInForm, &d.Editable, &d.Builtin, &d.SortOrder,
			&d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("字段不存在")
	}
	return &d, err
}

// validate 校验字段定义。
func validate(d *Def) error {
	d.FieldKey = strings.TrimSpace(d.FieldKey)
	d.Label = strings.TrimSpace(d.Label)
	d.FieldType = strings.TrimSpace(d.FieldType)
	d.Options = strings.TrimSpace(d.Options)
	if !validKey(d.FieldKey) {
		return fmt.Errorf("字段键 %q 非法（字母开头，仅字母数字下划线，最多 64 字符）", d.FieldKey)
	}
	if d.Label == "" {
		return fmt.Errorf("显示名称不能为空")
	}
	if len([]rune(d.Label)) > 200 {
		return fmt.Errorf("显示名称过长（最多 200 字符）")
	}
	switch d.FieldType {
	case TypeText, TypeTextarea, TypeNumber, TypeSelect, TypeDate:
	default:
		return fmt.Errorf("不支持的字段类型 %q（text/textarea/number/select/date）", d.FieldType)
	}
	if d.FieldType == TypeSelect && d.Options != "" {
		var opts []string
		if err := json.Unmarshal([]byte(d.Options), &opts); err != nil {
			return fmt.Errorf("select 的 options 必须是 JSON 字符串数组，如 [\"A\",\"B\"]")
		}
	}
	return nil
}

// Create 新增字段定义。
func (r *Repo) Create(ctx context.Context, d *Def) (int64, error) {
	if err := validate(d); err != nil {
		return 0, err
	}
	var id int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO `+r.table()+`
			(field_key, label, field_type, options, show_in_list, show_in_form, editable, builtin, sort_order)
		VALUES ($1,$2,$3,$4,$5,$6,$7,false,$8) RETURNING id`,
		d.FieldKey, d.Label, d.FieldType, d.Options,
		d.ShowInList, d.ShowInForm, d.Editable, d.SortOrder).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("字段键「%s」已存在", d.FieldKey)
		}
		return 0, err
	}
	return id, nil
}

// Update 更新字段定义（内置字段的键不可改）。
func (r *Repo) Update(ctx context.Context, d *Def) error {
	existing, err := r.Get(ctx, d.ID)
	if err != nil {
		return err
	}
	if existing.Builtin {
		d.FieldKey = existing.FieldKey
	}
	if err := validate(d); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE `+r.table()+` SET
			field_key=$1, label=$2, field_type=$3, options=$4,
			show_in_list=$5, show_in_form=$6, editable=$7, sort_order=$8, updated_at=NOW()
		WHERE id=$9`,
		d.FieldKey, d.Label, d.FieldType, d.Options,
		d.ShowInList, d.ShowInForm, d.Editable, d.SortOrder, d.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("字段键「%s」已被占用", d.FieldKey)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("字段不存在")
	}
	return nil
}

// Delete 删除字段定义（内置字段不可删）。
func (r *Repo) Delete(ctx context.Context, id int64) error {
	existing, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return fmt.Errorf("内置字段「%s」不可删除", existing.Label)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM `+r.table()+` WHERE id=$1`, id); err != nil {
		return err
	}
	// 删除定义时一并清理值，避免孤儿数据。
	if r.kind == KindUser {
		_, _ = r.db.ExecContext(ctx, `DELETE FROM user_field_values WHERE field_key=$1`, existing.FieldKey)
	} else {
		_, _ = r.db.ExecContext(ctx, `UPDATE hosts SET custom_fields = custom_fields - $1`, existing.FieldKey)
	}
	return nil
}

// ValidateValues 按定义校验并规范化字段值（用于保存时统一把关）。
//
// defs 来自 List；values 的键必须在 defs 内，否则忽略（防脏键写入 JSONB）。
func ValidateValues(defs []Def, values map[string]string) (map[string]string, error) {
	byKey := make(map[string]Def, len(defs))
	for _, d := range defs {
		byKey[d.FieldKey] = d
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		d, ok := byKey[k]
		if !ok {
			continue // 未知字段直接丢弃，避免写入未定义的键
		}
		v = strings.TrimSpace(v)
		if len([]rune(v)) > 4096 {
			return nil, fmt.Errorf("字段「%s」内容过长（最多 4096 字符）", d.Label)
		}
		switch d.FieldType {
		case TypeNumber:
			if v != "" {
				if _, err := strconv.ParseFloat(v, 64); err != nil {
					return nil, fmt.Errorf("字段「%s」必须是数字", d.Label)
				}
			}
		case TypeSelect:
			if v != "" && d.Options != "" {
				var opts []string
				if json.Unmarshal([]byte(d.Options), &opts) == nil {
					found := false
					for _, o := range opts {
						if o == v {
							found = true
							break
						}
					}
					if !found {
						return nil, fmt.Errorf("字段「%s」的值不在可选项中", d.Label)
					}
				}
			}
		case TypeDate:
			if v != "" {
				if _, err := time.Parse("2006-01-02", v); err != nil {
					return nil, fmt.Errorf("字段「%s」日期格式应为 YYYY-MM-DD", d.Label)
				}
			}
		}
		out[k] = v
	}
	return out, nil
}

// UserValues 读取指定用户的扩展字段值。
func (r *Repo) UserValues(ctx context.Context, username string) (map[string]string, error) {
	if r.kind != KindUser {
		return nil, fmt.Errorf("仅用户字段支持按用户读取")
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT field_key, value FROM user_field_values WHERE user_id=$1`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			out[k] = v
		}
	}
	return out, rows.Err()
}

// UserValuesForUsernames 批量读取指定用户的字段值，返回 username → (key → value)。
//
// 与 UserValuesAll 的区别：调用方传入当前租户可见的用户名，避免把其他租户的
// 字段值读出数据库（多租户列表接口使用）。
func (r *Repo) UserValuesForUsernames(ctx context.Context, usernames []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	if r.kind != KindUser {
		return nil, fmt.Errorf("仅用户字段支持该操作")
	}
	if len(usernames) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT user_id, field_key, value FROM user_field_values WHERE user_id = ANY($1)`,
		pq.Array(usernames))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid, k, v string
		if err := rows.Scan(&uid, &k, &v); err != nil {
			return nil, err
		}
		if out[uid] == nil {
			out[uid] = map[string]string{}
		}
		out[uid][k] = v
	}
	return out, rows.Err()
}

// UserValuesAll 一次性读取全部用户字段值，返回 username → (key → value)。
//
// 人员列表用：避免按用户逐个查询造成 N+1。
func (r *Repo) UserValuesAll(ctx context.Context) (map[string]map[string]string, error) {
	if r.kind != KindUser {
		return nil, fmt.Errorf("仅用户字段支持该操作")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT user_id, field_key, value FROM user_field_values`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var uid, k, v string
		if err := rows.Scan(&uid, &k, &v); err != nil {
			return nil, err
		}
		if out[uid] == nil {
			out[uid] = map[string]string{}
		}
		out[uid][k] = v
	}
	return out, rows.Err()
}

// SaveUserValues 保存用户扩展字段值（只写传入的键）。
func (r *Repo) SaveUserValues(ctx context.Context, username string, values map[string]string) error {
	if r.kind != KindUser {
		return fmt.Errorf("仅用户字段支持按用户保存")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_field_values (user_id, field_key, value, updated_at)
			VALUES ($1,$2,$3,NOW())
			ON CONFLICT (user_id, field_key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()`,
			username, k, values[k]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveHostValues 保存主机扩展字段（合并进 hosts.custom_fields）。
func (r *Repo) SaveHostValues(ctx context.Context, hostID int64, values map[string]string) error {
	if r.kind != KindHost {
		return fmt.Errorf("仅主机字段支持按主机保存")
	}
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE hosts SET custom_fields = COALESCE(custom_fields,'{}'::jsonb) || $2::jsonb,
		        updated_at = NOW()
		 WHERE id=$1`, hostID, string(b))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("主机不存在")
	}
	return nil
}

// isUniqueViolation 判断 PG 唯一约束冲突。
func isUniqueViolation(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "duplicate key") ||
		strings.Contains(err.Error(), "23505"))
}
