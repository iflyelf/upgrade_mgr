// Package scope 提供租户数据隔离的统一 SQL 作用域构造。
//
// 设计目标：让「租户过滤」只有一处实现，避免各业务查询各写一套导致漏过滤。
// 使用约定：所有面向租户数据的查询都必须经由本包构造 WHERE 条件，
// 不得手写 tenant_id 过滤。
package scope

import (
	"fmt"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// Scope 描述一次查询的租户可见范围。
type Scope struct {
	// TenantID 当前用户所属租户。
	TenantID int64
	// CrossTenant 为 true 时可见全部租户（仅 superadmin）。
	CrossTenant bool
}

// New 构造作用域。crossTenant 只应来自 auth.User.IsSuperAdmin()。
func New(tenantID int64, crossTenant bool) Scope {
	if tenantID <= 0 {
		tenantID = 1
	}
	return Scope{TenantID: tenantID, CrossTenant: crossTenant}
}

// Where 返回租户过滤条件与参数。
//
// table 为表别名或表名（如 "components" 或 "t"），startIdx 为占位符起始序号。
// 跨租户时返回恒真条件，保证调用方 SQL 结构不变（无需分支拼接）。
func (s Scope) Where(table string, startIdx int) (string, []any) {
	if s.CrossTenant {
		return "TRUE", nil
	}
	col := "tenant_id"
	if table != "" {
		col = table + ".tenant_id"
	}
	return fmt.Sprintf("%s = $%d", col, startIdx), []any{s.TenantID}
}

// Allows 判断某条记录所属租户是否在可见范围内。
// 用于单条记录读取后的归属校验（防越权读取他人资源）。
func (s Scope) Allows(recordTenantID int64) bool {
	if s.CrossTenant {
		return true
	}
	return recordTenantID == s.TenantID
}

// TenantForWrite 返回写入新记录时应使用的 tenant_id。
//
// 即便是 superadmin，新建资源也归属其自身租户，避免产生无主数据。
func (s Scope) TenantForWrite() int64 { return s.TenantID }

// ForUser 由认证用户推导查询作用域，是「用户 → 租户可见范围」的唯一转换点。
//
// 安全语义（fail-close）：user 为 nil（未认证或中间件未正确注入）时，
// 不返回跨租户作用域，而是限定到不存在的租户 -1，使查询命中空集。
// 这样即使调用方漏掉鉴权，也不会泄露任何租户数据。
func ForUser(u *auth.User) Scope {
	if u == nil {
		return Scope{TenantID: -1, CrossTenant: false}
	}
	if u.IsSuperAdmin() {
		return Scope{TenantID: normalizeTenant(u.TenantID), CrossTenant: true}
	}
	return Scope{TenantID: normalizeTenant(u.TenantID), CrossTenant: false}
}

// normalizeTenant 把非法租户号归一为默认租户 1（仅用于已认证用户）。
func normalizeTenant(id int64) int64 {
	if id <= 0 {
		return 1
	}
	return id
}

// System 返回后台任务（编排器、维护、Agent 网关）使用的作用域。
//
// 后台任务无用户上下文，需要处理全部租户的数据，因此允许跨租户。
// 注意：仅供内部调度使用，绝不可用于响应用户请求。
func System() Scope {
	return Scope{TenantID: 1, CrossTenant: true}
}
