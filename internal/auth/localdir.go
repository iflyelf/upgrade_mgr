package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/lib/pq"
)

// LocalAccount 本地账号（local_users）中的租户与角色信息。
//
// SSO 只负责证明「你是谁」，权限仍以本系统 local_users 为准：
//   - 已有本地账号：沿用其租户/角色/禁用状态（可在用户管理页调整）；
//   - 首次登录：自动建档，避免登录后「用户管理里只有 admin」。
type LocalAccount struct {
	DisplayName string
	TenantID    int64
	Roles       []string
	IsAdmin     bool
	Disabled    bool
}

// LocalAccountResolver 在 SSO 登录成功后解析本地账号。
//
// casdoorAdmin 来自 Casdoor 的 isAdmin，仅在首次建档时用于决定初始角色，
// 已有本地账号不会被它覆盖（本地角色是权威来源）。
type LocalAccountResolver func(ctx context.Context, username, displayName string, casdoorAdmin bool) (*LocalAccount, error)

// LocalDirectory 基于 local_users 表解析/创建本地账号。
type LocalDirectory struct{ db *sql.DB }

// NewLocalDirectory 创建本地账号目录。
func NewLocalDirectory(db *sql.DB) *LocalDirectory { return &LocalDirectory{db: db} }

// Resolve 返回本地账号；不存在时按 SSO 身份自动建档。
//
// 角色策略：
//   - 已有账号：完全沿用本地 roles/tenant_id（管理员可在页面调整，本地为准）；
//   - 首次登录：普通用户 role=user；Casdoor/FlyIAM 管理员自动获得
//     admin + superadmin，避免「切换 SSO 后系统设置菜单消失」；
//   - 首次建档后角色改由本地维护，Casdoor 的 isAdmin 不再覆盖。
func (d *LocalDirectory) Resolve(ctx context.Context, username, displayName string, casdoorAdmin bool) (*LocalAccount, error) {
	acc, err := d.query(ctx, username)
	if err != nil {
		return nil, err
	}
	if acc != nil {
		// 显示名以 Casdoor/FlyIAM 为准：每次登录同步，避免本地旧值长期覆盖。
		if dn := strings.TrimSpace(displayName); dn != "" && dn != strings.TrimSpace(acc.DisplayName) {
			if _, err := d.db.ExecContext(ctx,
				`UPDATE local_users SET display_name=$2, updated_at=NOW() WHERE username=$1`,
				username, dn); err == nil {
				acc.DisplayName = dn
			}
		}
		return acc, nil
	}

	roles := []string{RoleUser}
	if casdoorAdmin {
		// superadmin 决定「系统设置 / 外部文件 / 缓存 / Consul 同步」等全局菜单，
		// SSO 管理员首次登录必须拿到，否则页面上这些入口会整组消失。
		roles = []string{RoleAdmin, RoleSuperAdmin}
	}
	if _, err := d.db.ExecContext(ctx, `
		INSERT INTO local_users (username, password_hash, display_name, is_admin, tenant_id, roles)
		VALUES ($1,'',$2,$3,1,$4)
		ON CONFLICT (username) DO NOTHING`,
		username, strings.TrimSpace(displayName), containsAnyRole(roles, RoleAdmin, RoleSuperAdmin),
		pq.StringArray(roles)); err != nil {
		return nil, fmt.Errorf("创建本地账号失败: %w", err)
	}
	log.Printf("ℹ️ SSO 首次登录已自动创建本地账号: %s（角色: %s）", username, strings.Join(roles, ","))

	// 并发登录时可能已被其他副本创建：重新查询拿到真实记录（含他人设置的角色）。
	acc, err = d.query(ctx, username)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, fmt.Errorf("创建本地账号后未能读取: %s", username)
	}
	return acc, nil
}

// query 读取本地账号；不存在返回 (nil, nil)。
func (d *LocalDirectory) query(ctx context.Context, username string) (*LocalAccount, error) {
	var (
		acc               LocalAccount
		roles             pq.StringArray
		display           sql.NullString
		tenantID          sql.NullInt64
		isAdmin, disabled bool
	)
	err := d.db.QueryRowContext(ctx, `
		SELECT COALESCE(display_name,''), COALESCE(tenant_id,1),
		       COALESCE(roles, ARRAY['user']::text[]), is_admin, disabled
		FROM local_users WHERE username=$1`, username).
		Scan(&display, &tenantID, &roles, &isAdmin, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询本地账号失败: %w", err)
	}
	acc.DisplayName = display.String
	acc.TenantID = tenantID.Int64
	if acc.TenantID <= 0 {
		acc.TenantID = 1
	}
	acc.Roles = []string(roles)
	if len(acc.Roles) == 0 {
		acc.Roles = []string{RoleUser}
	}
	// is_admin 与 roles 保持同步，兼容历史数据里两者不一致的情况。
	acc.IsAdmin = isAdmin || containsAnyRole(acc.Roles, RoleAdmin, RoleSuperAdmin)
	acc.Disabled = disabled
	return &acc, nil
}

// containsAnyRole 判断角色列表是否包含任意目标角色。
func containsAnyRole(roles []string, want ...string) bool {
	for _, r := range roles {
		for _, w := range want {
			if r == w {
				return true
			}
		}
	}
	return false
}
