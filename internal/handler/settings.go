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
		httpx.OK(w, map[string]any{"applied": applied})
	}
}
