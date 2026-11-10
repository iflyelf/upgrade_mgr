package fielddef

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/flyiam"
)

// SyncUserDefs 按 field_key 幂等 upsert 用户字段定义。
//
// 语义（与 FlyIAM 对齐）：
//   - 只更新 FlyIAM 返回的展示属性，本地新增字段不会被删除；
//   - 内置字段保留本地 builtin 标记，不会被远端覆盖；
//   - 返回新增/更新/总拉取数，供页面与自动同步日志展示。
func SyncUserDefs(ctx context.Context, db *sql.DB, remote []flyiam.UserFieldDef) (int, int, int, error) {
	repo, err := NewRepo(db, KindUser)
	if err != nil {
		return 0, 0, 0, err
	}
	local, err := repo.List(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	byKey := make(map[string]Def, len(local))
	for _, d := range local {
		byKey[d.FieldKey] = d
	}

	added, updated := 0, 0
	for _, rf := range remote {
		def := Def{
			FieldKey:   strings.TrimSpace(rf.FieldKey),
			Label:      rf.Label,
			FieldType:  NormalizeRemoteType(rf.FieldType),
			Options:    rf.Options,
			ShowInList: rf.ShowInList,
			ShowInForm: rf.ShowInForm,
			Editable:   rf.Editable,
			SortOrder:  rf.SortOrder,
		}
		if def.FieldKey == "" {
			continue
		}
		if old, ok := byKey[def.FieldKey]; ok {
			def.ID = old.ID
			def.Builtin = old.Builtin
			if sameDef(old, def) {
				continue
			}
			if err := repo.Update(ctx, &def); err != nil {
				return added, updated, len(remote), fmt.Errorf("更新字段 %s 失败: %w", def.FieldKey, err)
			}
			updated++
		} else {
			if _, err := repo.Create(ctx, &def); err != nil {
				return added, updated, len(remote), fmt.Errorf("新增字段 %s 失败: %w", def.FieldKey, err)
			}
			added++
		}
	}
	return added, updated, len(remote), nil
}

// NormalizeRemoteType 把 FlyIAM 的字段类型映射到本地允许值。
func NormalizeRemoteType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case TypeTextarea:
		return TypeTextarea
	case TypeNumber:
		return TypeNumber
	case TypeSelect:
		return TypeSelect
	case TypeDate:
		return TypeDate
	default:
		return TypeText
	}
}

// sameDef 判断两个定义的展示属性是否一致。
func sameDef(a, b Def) bool {
	return a.Label == b.Label &&
		a.FieldType == b.FieldType &&
		a.Options == b.Options &&
		a.ShowInList == b.ShowInList &&
		a.ShowInForm == b.ShowInForm &&
		a.Editable == b.Editable &&
		a.SortOrder == b.SortOrder
}
