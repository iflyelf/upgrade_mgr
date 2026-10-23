package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// SyncDepartmentsFromFlyIAM 从 FlyIAM 同步组织树（管理员）。
//
// 上游由用户部门属性推导部门；本接口整体替换本地快照（事务）。
func SyncDepartmentsFromFlyIAM(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client := flyiamClient(s)
		if client == nil {
			httpx.Fail(w, http.StatusServiceUnavailable,
				"未配置 FlyIAM 地址与服务令牌，请先到「设置 → FlyIAM」填写")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()

		deps, err := client.FetchDepartments(ctx)
		if err != nil {
			auditOp(s, r, audit.ActionFlyIAMSync, "department", "", "", err)
			httpx.Fail(w, http.StatusBadGateway, "从 FlyIAM 拉取组织架构失败: "+err.Error())
			return
		}
		// 上游返回空组织时不清空本地快照：常见于数据源部门字段映射缺失，
		// 直接替换会让页面突然变空，排查期间保留旧数据更安全。
		if len(deps) == 0 {
			err := errors.New("FlyIAM 未返回组织数据，已保留本地快照；请检查数据源的部门字段映射")
			auditOp(s, r, audit.ActionFlyIAMSync, "department", "", "", err)
			httpx.Fail(w, http.StatusBadGateway, err.Error())
			return
		}
		n, err := orgService(s).ReplaceDepartments(r.Context(), deps)
		auditOpDetails(s, r, audit.ActionFlyIAMSync, "department", "",
			"replace", err, nil, map[string]any{"total": n, "upstream": len(deps)})
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, map[string]any{"total": n})
	}
}

// DepartmentList 返回本地组织树快照（供「人员组织 → 组织架构」展示）。
func DepartmentList(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := orgService(s).ListDepartments(r.Context())
		if err != nil {
			httpx.FailErr(w, http.StatusInternalServerError, err)
			return
		}
		httpx.OK(w, list)
	}
}
