package handler

import (
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"net/http"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/setting"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// ListSettings 返回全部可配置项（secret 脱敏）。
func ListSettings(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, s.Settings.View())
	}
}

// UpdateSettings 保存设置并即时生效；涉及 Consul 的变更触发热重载。
func UpdateSettings(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var kv map[string]string
		if err := httpx.DecodeJSON(r, &kv); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		applied, err := s.Settings.Apply(r.Context(), kv)
		if err != nil {
			auditOp(s, r, audit.ActionSettingUpdate, "setting", "", "", err)
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		auditOp(s, r, audit.ActionSettingUpdate, "setting", "", strings.Join(applied, ","), nil)
		if setting.NeedsConsulReload(applied) {
			s.ReloadConsul()
		}
		if setting.NeedsCacheReload(applied) {
			s.ReloadCache()
		}
		if setting.NeedsArtifactReload(applied) {
			s.ReloadArtifactSettings()
		}
		httpx.OK(w, map[string]any{"applied": applied})
	}
}

// ResetSetting 删除页面保存的值，恢复为配置文件/环境变量/默认值：DELETE /api/settings/{key}。
//
// 页面设置优先级最高；不提供恢复入口时，一旦在页面保存过，之后修改配置文件将不再生效。
func ResetSetting(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.PathValue("key"))
		if err := s.Settings.Reset(r.Context(), key); err != nil {
			auditOp(s, r, audit.ActionSettingReset, "setting", key, key, err)
			httpx.FailErr(w, http.StatusBadRequest, err)
			return
		}
		auditOp(s, r, audit.ActionSettingReset, "setting", key, key, nil)
		applied := []string{key}
		if setting.NeedsConsulReload(applied) {
			s.ReloadConsul()
		}
		if setting.NeedsCacheReload(applied) {
			s.ReloadCache()
		}
		if setting.NeedsArtifactReload(applied) {
			s.ReloadArtifactSettings()
		}
		base, _ := s.Settings.BaseValue(key)
		httpx.OK(w, map[string]any{"key": key, "value": base})
	}
}
