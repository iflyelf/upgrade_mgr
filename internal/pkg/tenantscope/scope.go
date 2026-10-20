// Package tenantscope 提供租户作用域，全项目唯一实现（避免多处实现发散）。
//
// 设计原则：
//   - 作用域只能由「已认证用户」推导，绝不接受客户端传入的 tenant_id；
//   - superadmin 可跨租户（All=true），其余一律限定在自身租户；
//   - 提供 SQL 片段拼装助手，保证所有业务查询以统一方式过滤。
package tenantscope

import (
	"context"
	"fmt"
)

// Scope 租户作用域。
type Scope struct {
	TenantID int64 // 用户所属租户
	All      bool  // true=可跨租户（superadmin）
}

type ctxKey struct{}

// With 将作用域放入 context。
func With(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// From 取出作用域。第二个返回值表示是否存在。
func From(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(ctxKey{}).(Scope)
	return s, ok
}

// FromOrDeny 取出作用域；缺失时返回一个「不匹配任何行」的安全作用域。
//
// 安全取向：context 中无作用域说明调用方未经过租户中间件，
// 此时应当查不到任何数据（fail-closed），而不是退化为「查全部」。
func FromOrDeny(ctx context.Context) Scope {
	if s, ok := From(ctx); ok {
		return s
	}
	return Scope{TenantID: -1, All: false}
}

// Where 返回租户过滤 SQL 与参数。
//
// table 为表别名或表名（可为空表示不加前缀）。startIdx 为占位符起始序号。
// 跨租户时返回恒真条件，不产生参数。
func (s Scope) Where(table string, startIdx int) (string, []any) {
	if s.All {
		return "TRUE", nil
	}
	col := "tenant_id"
	if table != "" {
		col = table + ".tenant_id"
	}
	return fmt.Sprintf("%s = $%d", col, startIdx), []any{s.TenantID}
}

// Allows 判断给定资源租户是否在作用域内。
func (s Scope) Allows(tenantID int64) bool {
	if s.All {
		return true
	}
	return s.TenantID == tenantID
}
