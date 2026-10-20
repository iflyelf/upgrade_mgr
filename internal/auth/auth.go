// Package auth 提供可切换的认证抽象：builtin（内置 bcrypt 账号 + 签名会话）与
// casdoor（FlyIAM SSO，P3 完整实现）。控制台按 config.Auth.Provider 选择实现。
package auth

import (
	"context"
	"errors"
	"net/http"
)

// User 认证后的用户身份。
type User struct {
	ID          string
	Username    string
	DisplayName string
	IsAdmin     bool
	// TenantID 所属租户；业务查询据此做数据隔离。
	TenantID int64
	// Roles 角色列表，如 ["admin"]、["superadmin"]；superadmin 可跨租户。
	Roles []string
}

// 角色常量：避免字面量散落导致拼写不一致。
const (
	RoleUser       = "user"
	RoleAdmin      = "admin"
	RoleSuperAdmin = "superadmin"
)

// HasRole 是否拥有指定角色。
func (u *User) HasRole(role string) bool {
	for _, r := range u.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// IsSuperAdmin 超级管理员：可跨租户访问全部数据。
func (u *User) IsSuperAdmin() bool { return u.HasRole(RoleSuperAdmin) }

// CanAccessTenant 是否允许访问指定租户的数据。
func (u *User) CanAccessTenant(tenantID int64) bool {
	if u.IsSuperAdmin() {
		return true
	}
	return u.TenantID == tenantID
}

// ErrUnauthorized 未认证/凭据无效。
var ErrUnauthorized = errors.New("未认证或凭据无效")

// Provider 认证提供方抽象。
type Provider interface {
	// Name 提供方标识：builtin / casdoor。
	Name() string
	// Login 校验凭据（builtin：用户名+密码；casdoor：不使用，走 OAuth 跳转）。
	Login(ctx context.Context, username, password string) (*User, string, error)
	// Authenticate 从请求中解析并校验当前用户（cookie / bearer）。
	Authenticate(r *http.Request) (*User, error)
	// SetSession 登录成功后写入会话（builtin：签名 cookie）。
	SetSession(w http.ResponseWriter, token string)
	// ClearSession 注销。
	ClearSession(w http.ResponseWriter)
	// LoginURL 返回登录跳转地址（casdoor 用；builtin 返回空表示本地表单登录）。
	LoginURL(state string) string
}

type ctxKey int

const userKey ctxKey = 0

// WithUser 把用户放入 context。
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// FromContext 取出当前用户。
func FromContext(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(userKey).(*User)
	return u, ok
}
