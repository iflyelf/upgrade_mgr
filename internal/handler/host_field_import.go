package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/logic/fielddef"
	"github.com/iflyelf/upgrade_mgr/internal/logic/hostfieldmap"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// importKeyRequest 一个待导入的来源键。
type importKeyRequest struct {
	// SourceKey 外部来源的键（扫描结果里的 key）。
	SourceKey string `json:"sourceKey"`
	// Label 字段显示名；留空时用 SourceKey。
	Label string `json:"label"`
	// TargetKind column=映射到内置列；custom=创建自定义字段并映射（默认）。
	TargetKind string `json:"targetKind"`
	// TargetKey 目标列名或自定义字段键；留空时由 SourceKey 规范化而来。
	TargetKey string `json:"targetKey"`
	// ShowInList 自定义字段是否作为主机列表的一列。
	ShowInList bool `json:"showInList"`
	// Overwrite 同步时是否覆盖本地已有值（默认只补空值）。
	Overwrite bool `json:"overwrite"`
}

// ImportHostFieldKeys 把扫描到的来源键导入为「字段定义 + 映射」（管理员）。
//
// 这是「扫描 → 人工勾选 → 导入」流程的最后一步：绝不自动创建字段，
// 必须由管理员明确选择要哪些键，避免外部 Meta 污染字段定义。
//
// 语义：
//   - targetKind=custom（默认）：字段不存在则创建（show_in_form=true），
//     已存在则复用，不改动既有定义；
//   - targetKind=column：只建映射，目标必须在内置列白名单内；
//   - 映射按 (source, source_key) 幂等：已存在则跳过，不覆盖已有配置。
//
// 任一条目失败只跳过该条并记入 failed，其余继续，便于批量勾选时部分成功。
func ImportHostFieldKeys(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Source string             `json:"source"`
			Keys   []importKeyRequest `json:"keys"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		source := strings.TrimSpace(req.Source)
		if source == "" {
			source = hostfieldmap.SourceConsul
		}
		if source != hostfieldmap.SourceConsul && source != hostfieldmap.SourceConsulMgr {
			httpx.Fail(w, http.StatusBadRequest, "来源不支持（仅 consul / consul_mgr）")
			return
		}
		if len(req.Keys) == 0 {
			httpx.Fail(w, http.StatusBadRequest, "没有勾选任何来源键")
			return
		}

		fieldRepo, ok := fieldDefRepo(s, fielddef.KindHost)
		if !ok {
			httpx.Fail(w, http.StatusInternalServerError, "主机字段仓储初始化失败")
			return
		}
		defs, err := fieldRepo.List(r.Context())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		existingFields := map[string]bool{}
		for _, d := range defs {
			existingFields[d.FieldKey] = true
		}

		mapRepo := hostfieldmap.NewRepo(s.DB)
		existingMaps, err := mapRepo.List(r.Context(), source)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		mapped := map[string]bool{}
		for _, m := range existingMaps {
			mapped[m.SourceKey] = true
		}

		// sortBase 让新建映射排在已有映射之后，保持页面顺序稳定。
		sortBase := 100 + len(existingMaps)*10

		var createdFields, createdMaps, skipped int
		var failed []map[string]string
		for i, k := range req.Keys {
			sourceKey := strings.TrimSpace(k.SourceKey)
			if sourceKey == "" {
				continue
			}
			if mapped[sourceKey] {
				// 已有映射：保留管理员既有配置，不覆盖。
				skipped++
				continue
			}

			kind := strings.ToLower(strings.TrimSpace(k.TargetKind))
			if kind == "" {
				kind = hostfieldmap.KindCustom
			}
			targetKey := strings.TrimSpace(k.TargetKey)
			if targetKey == "" {
				targetKey = normalizeFieldKey(sourceKey)
			}
			if targetKey == "" {
				failed = append(failed, map[string]string{
					"key": sourceKey, "error": "无法由来源键推导出合法的字段键，请手工填写",
				})
				continue
			}

			// 自定义字段：不存在则创建。
			if kind == hostfieldmap.KindCustom && !existingFields[targetKey] {
				label := strings.TrimSpace(k.Label)
				if label == "" {
					label = sourceKey
				}
				def := fielddef.Def{
					FieldKey: targetKey, Label: label,
					FieldType: fielddef.TypeText,
					// 导入的字段默认可在编辑表单查看/修改；是否上列表由勾选决定。
					ShowInList: k.ShowInList, ShowInForm: true, Editable: true,
					SortOrder: sortBase + i,
				}
				if _, err := fieldRepo.Create(r.Context(), &def); err != nil {
					failed = append(failed, map[string]string{
						"key": sourceKey, "error": "创建字段失败: " + err.Error(),
					})
					continue
				}
				existingFields[targetKey] = true
				createdFields++
			}

			m := hostfieldmap.Mapping{
				Source: source, SourceKey: sourceKey,
				TargetKind: kind, TargetKey: targetKey,
				Enabled: true, Overwrite: k.Overwrite,
				SortOrder: sortBase + i,
			}
			if _, err := mapRepo.Create(r.Context(), &m); err != nil {
				failed = append(failed, map[string]string{
					"key": sourceKey, "error": "创建映射失败: " + err.Error(),
				})
				continue
			}
			mapped[sourceKey] = true
			createdMaps++
		}

		auditOpDetails(s, r, audit.ActionHostFieldMapImport, "host_field_mapping", source,
			fmt.Sprintf("fields=%d mappings=%d skipped=%d failed=%d",
				createdFields, createdMaps, skipped, len(failed)), nil,
			map[string]any{
				"source": source, "created_fields": createdFields,
				"created_mappings": createdMaps, "skipped": skipped,
				"failed": len(failed),
			}, nil)

		httpx.OK(w, map[string]any{
			"created_fields": createdFields, "created_mappings": createdMaps,
			"skipped": skipped, "failed": failed,
		})
	}
}

// normalizeFieldKey 把外部来源键规范化为合法的自定义字段键。
//
// 规则（与 fielddef 的 ^[A-Za-z][A-Za-z0-9_]{0,63}$ 对齐）：
//   - 去掉平台保留键的 __ 前缀；
//   - 非字母数字字符（- . 等）转为下划线；
//   - 首字符非字母时补 f_ 前缀；
//   - 超长截断到 64 字符。
//
// 推导失败（如全是非法字符）返回空串，由调用方要求人工填写。
func normalizeFieldKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "__")
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return ""
	}
	first := out[0]
	if !(first >= 'a' && first <= 'z') && !(first >= 'A' && first <= 'Z') {
		out = "f_" + out
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
