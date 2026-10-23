package handler

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/fielddef"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/flyiam"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/rbac"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// 生成随机密码用的字符集：去掉 0/O、1/l/I 等易混淆字符。
const provisionPasswordCharset = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789@#%"

// generatePassword 生成 n 位随机密码（crypto/rand，无模偏差）。
func generatePassword(n int) (string, error) {
	if n <= 0 {
		n = 16
	}
	max := big.NewInt(int64(len(provisionPasswordCharset)))
	var b strings.Builder
	b.Grow(n)
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(provisionPasswordCharset[idx.Int64()])
	}
	return b.String(), nil
}

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

// FlyIAMUsers 从 FlyIAM 读取用户列表（只读展示）。
//
// 用途：「人员组织 → 人员信息」可与 FlyIAM 的人员对照；本系统不写回用户数据，
// 用户增删改仍以 Casdoor / FlyIAM 为准。
func FlyIAMUsers(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client := flyiamClient(s)
		if client == nil {
			httpx.Fail(w, http.StatusServiceUnavailable,
				"未配置 FlyIAM 地址与服务令牌，请先到「设置 → FlyIAM」填写")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		users, err := client.FetchUsers(ctx)
		if err != nil {
			httpx.Fail(w, http.StatusBadGateway, "从 FlyIAM 拉取用户失败: "+err.Error())
			return
		}
		httpx.OK(w, map[string]any{"users": users, "total": len(users), "readonly": true})
	}
}

// FlyIAMProvisionLocalUser 为 FlyIAM 用户一键创建本地账号（管理员）。
//
// 语义：
//   - 用户名必须存在于 FlyIAM（防止手填任意账号）；
//   - 本地已存在同名账号时返回 409，绝不覆盖密码；
//   - 未提供密码时生成一次性随机密码，仅在本次响应返回；
//   - sync_fields 为 true 时，把 FlyIAM properties 中与本地用户字段定义同名的值写入扩展字段。
func FlyIAMProvisionLocalUser(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := strings.TrimSpace(r.PathValue("username"))
		if username == "" {
			httpx.Fail(w, http.StatusBadRequest, "用户名为空")
			return
		}
		var req struct {
			Password   string   `json:"password"`
			Roles      []string `json:"roles"`
			SyncFields bool     `json:"sync_fields"`
		}
		if r.ContentLength != 0 {
			if err := httpx.DecodeJSON(r, &req); err != nil && err != io.EOF {
				httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
				return
			}
		}

		client := flyiamClient(s)
		if client == nil {
			httpx.Fail(w, http.StatusServiceUnavailable,
				"未配置 FlyIAM 地址与服务令牌，请先到「设置 → FlyIAM」填写")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		remoteUsers, err := client.FetchUsers(ctx)
		if err != nil {
			httpx.Fail(w, http.StatusBadGateway, "从 FlyIAM 拉取用户失败: "+err.Error())
			return
		}
		var remote *flyiam.RemoteUser
		for i := range remoteUsers {
			if remoteUsers[i].Username == username {
				remote = &remoteUsers[i]
				break
			}
		}
		if remote == nil {
			httpx.Fail(w, http.StatusNotFound, "FlyIAM 中不存在用户 "+username)
			return
		}

		var exists bool
		if err := s.DB.QueryRowContext(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM local_users WHERE username=$1)`, username).Scan(&exists); err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		if exists {
			httpx.Fail(w, http.StatusConflict, "本地已存在账号 "+username+"，无需重复创建")
			return
		}

		password := req.Password
		generated := false
		if password == "" {
			password, err = generatePassword(16)
			if err != nil {
				httpx.FailErr(w, http.StatusInternalServerError, err)
				return
			}
			generated = true
		}
		roles := req.Roles
		if len(roles) == 0 {
			roles = []string{"user"}
		}
		id, err := s.RBAC.Create(r.Context(), actorOf(r), rbac.CreateRequest{
			Username: username,
			Password: password,
			Display:  strings.TrimSpace(remote.DisplayName),
			Roles:    roles,
		})
		auditOp(s, r, audit.ActionUserCreate, "user", username, username, err)
		if err != nil {
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}

		// 可选：把 FlyIAM 属性写入本地用户扩展字段（仅写已定义的键）。
		synced := 0
		if req.SyncFields && len(remote.Properties) > 0 {
			if repo, repoErr := fielddef.NewRepo(s.DB, fielddef.KindUser); repoErr == nil {
				defs, listErr := repo.List(r.Context())
				if listErr == nil {
					values := map[string]string{}
					for _, d := range defs {
						if v, ok := remote.Properties[d.FieldKey]; ok && strings.TrimSpace(v) != "" {
							values[d.FieldKey] = strings.TrimSpace(v)
						}
					}
					// 逐字段校验：FlyIAM 的某个值不合本地定义时只跳过该字段，
					// 不能让整批同步静默失败。
					clean := map[string]string{}
					for k, v := range values {
						one, vErr := fielddef.ValidateValues(defs, map[string]string{k: v})
						if vErr != nil {
							continue
						}
						for kk, vv := range one {
							clean[kk] = vv
						}
					}
					if len(clean) > 0 {
						if saveErr := repo.SaveUserValues(r.Context(), username, clean); saveErr == nil {
							synced = len(clean)
						}
					}
				}
			}
		}

		resp := map[string]any{
			"id": id, "username": username, "generated": generated, "synced_fields": synced,
		}
		if generated {
			resp["password"] = password
			resp["note"] = "随机密码仅本次显示，请立即转告用户登录后修改"
		}
		httpx.OK(w, resp)
	}
}
