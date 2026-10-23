package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/fielddef"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/flyiam"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// flyiamClient 按当前设置构造 FlyIAM 客户端（未配置返回 nil）。
func flyiamClient(s *svc.ServiceContext) *flyiam.Client {
	cfg := externalBinaryConfig(s)
	if cfg == nil {
		return nil
	}
	return flyiam.NewClient(cfg.FlyIAM.Endpoint, cfg.FlyIAM.ServiceToken)
}

// SyncUserFieldsFromFlyIAM 从 FlyIAM 同步用户字段定义（管理员）。
//
// 语义：按 field_key 幂等 upsert；本地新增的字段不会被删除（避免误删自定义字段）。
// 内置字段只更新展示属性（保留 builtin 标记与字段键）。
func SyncUserFieldsFromFlyIAM(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client := flyiamClient(s)
		if client == nil {
			httpx.Fail(w, http.StatusServiceUnavailable,
				"未配置 FlyIAM 地址与服务令牌，请先到「设置 → FlyIAM」填写")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		remote, err := client.FetchUserFields(ctx)
		if err != nil {
			auditOp(s, r, audit.ActionFlyIAMSync, "user_field", "", "", err)
			httpx.Fail(w, http.StatusBadGateway, "从 FlyIAM 拉取字段失败: "+err.Error())
			return
		}

		repo, repoErr := fielddef.NewRepo(s.DB, fielddef.KindUser)
		if repoErr != nil {
			httpx.FailErr(w, http.StatusInternalServerError, repoErr)
			return
		}
		local, err := repo.List(ctx)
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		byKey := make(map[string]fielddef.Def, len(local))
		for _, d := range local {
			byKey[d.FieldKey] = d
		}

		added, updated := 0, 0
		for _, rf := range remote {
			def := fielddef.Def{
				FieldKey:   strings.TrimSpace(rf.FieldKey),
				Label:      rf.Label,
				FieldType:  normalizeFieldType(rf.FieldType),
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
				// 本地存在：只更新远端提供的展示属性；内置标记保留本地值。
				def.ID = old.ID
				def.Builtin = old.Builtin
				if sameFieldDef(old, def) {
					continue
				}
				if err := repo.Update(ctx, &def); err != nil {
					httpx.Fail(w, http.StatusBadRequest,
						fmt.Sprintf("更新字段 %s 失败: %v", def.FieldKey, err))
					return
				}
				updated++
			} else {
				if _, err := repo.Create(ctx, &def); err != nil {
					httpx.Fail(w, http.StatusBadRequest,
						fmt.Sprintf("新增字段 %s 失败: %v", def.FieldKey, err))
					return
				}
				added++
			}
		}

		auditOpDetails(s, r, audit.ActionFlyIAMSync, "user_field", "",
			fmt.Sprintf("added=%d updated=%d total=%d", added, updated, len(remote)), nil,
			map[string]any{"added": added, "updated": updated, "total": len(remote)}, nil)
		httpx.OK(w, map[string]any{
			"added": added, "updated": updated, "total": len(remote),
		})
	}
}

// TestFlyIAM 测试 FlyIAM 连通性（管理员）。
func TestFlyIAM(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client := flyiamClient(s)
		if client == nil {
			httpx.Fail(w, http.StatusBadRequest, "未配置 FlyIAM 地址与服务令牌")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		start := time.Now()
		err := client.HealthCheck(ctx)
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			httpx.OK(w, map[string]any{
				"ok": false, "error": err.Error(), "elapsed_ms": elapsed,
				"hint": "确认 FlyIAM 地址可达、服务令牌有效，并允许本服务访问 /api/casdoor/app-credentials",
			})
			return
		}
		creds, credErr := client.FetchAppCredentials(ctx)
		resp := map[string]any{"ok": true, "elapsed_ms": elapsed}
		if credErr == nil && creds != nil {
			// 只回显非敏感字段，绝不返回 ClientSecret。
			resp["application"] = creds.Application
			resp["organization"] = creds.Organization
			resp["client_id_present"] = creds.ClientID != ""
		}
		httpx.OK(w, resp)
	}
}

// normalizeFieldType 把 FlyIAM 的字段类型映射到本地允许值。
func normalizeFieldType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case fielddef.TypeTextarea:
		return fielddef.TypeTextarea
	case fielddef.TypeNumber:
		return fielddef.TypeNumber
	case fielddef.TypeSelect:
		return fielddef.TypeSelect
	case fielddef.TypeDate:
		return fielddef.TypeDate
	default:
		return fielddef.TypeText
	}
}

// sameFieldDef 判断两个定义的展示属性是否一致（键不同不算差异）。
func sameFieldDef(a, b fielddef.Def) bool {
	return a.Label == b.Label &&
		a.FieldType == b.FieldType &&
		a.Options == b.Options &&
		a.ShowInList == b.ShowInList &&
		a.ShowInForm == b.ShowInForm &&
		a.Editable == b.Editable &&
		a.SortOrder == b.SortOrder
}
