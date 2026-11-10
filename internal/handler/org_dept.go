package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/logic/org"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/httpx"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// SyncDepartmentsFromFlyIAM 同步组织架构（管理员）。
//
// 数据源自 FlyIAM，但**不再调用 FlyIAM 的 /api/departments/export**：
// 该接口内部要串行遍历 3 万用户，通常超过反向代理超时并返回 503。
// 这里直接按 Casdoor 分页（应用凭据 Basic 认证）拉取用户，逐页用
// deptIdLv0/1/2、deptNameLv0/1/2 推导部门树，每次 HTTP 请求都很短。
func SyncDepartmentsFromFlyIAM(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dir := casdoorDirectory(s)
		if !dir.Valid() {
			httpx.Fail(w, http.StatusServiceUnavailable,
				"Casdoor 未配置，无法同步组织架构；请先在「设置 → Casdoor」补齐地址与应用凭据")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
		defer cancel()

		const pageSize = 1000
		const maxUsers = 200000
		builder := org.NewDeptBuilder()
		seen := 0
		for page := 1; page <= maxUsers/pageSize; page++ {
			users, total, err := dir.List(ctx, page, pageSize, "", "")
			if err != nil {
				auditOp(s, r, audit.ActionFlyIAMSync, "department", "", "", err)
				httpx.Fail(w, http.StatusBadGateway, "从 Casdoor 拉取用户失败: "+err.Error())
				return
			}
			for _, u := range users {
				builder.AddUser(u.Properties)
			}
			seen += len(users)
			// 本页不足 pageSize，或已达到总数：结束翻页。
			if len(users) < pageSize || (total > 0 && seen >= total) {
				break
			}
		}

		deps := builder.List()
		// 上游返回空组织时不清空本地快照：常见于数据源部门字段映射缺失，
		// 直接替换会让页面突然变空，排查期间保留旧数据更安全。
		if len(deps) == 0 {
			err := errors.New("Casdoor 用户未返回部门属性，已保留本地快照；请检查 FlyIAM 数据源的部门字段映射")
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
