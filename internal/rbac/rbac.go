// Package rbac 用户与租户管理：创建用户、分配租户与角色。
//
// 设计说明：角色判定与租户可见范围分别由 auth.User 与 pkg/scope 承担，
// 本包不再重复定义 User 类型，只负责「管理」这些属性的持久化，
// 避免同一概念出现两套实现而发散。
package rbac

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// Service 用户与租户管理服务。
type Service struct{ db *sql.DB }

// NewService 创建服务。
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// validRoles 允许分配的角色白名单。
// superadmin 具备跨租户权限，必须显式列出，防止任意字符串被写入 roles。
var validRoles = map[string]bool{
	auth.RoleUser:       true,
	auth.RoleAdmin:      true,
	auth.RoleSuperAdmin: true,
}

// normalizeRoles 校验并去重角色；空则默认 user。
func normalizeRoles(roles []string) ([]string, error) {
	if len(roles) == 0 {
		return []string{auth.RoleUser}, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if !validRoles[r] {
			return nil, fmt.Errorf("非法角色 %q（允许：user/admin/superadmin）", r)
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return []string{auth.RoleUser}, nil
	}
	return out, nil
}

// UserInfo 用户管理视图。
type UserInfo struct {
	ID       int64    `json:"id"`
	Username string   `json:"username"`
	Display  string   `json:"display_name"`
	TenantID int64    `json:"tenant_id"`
	Roles    []string `json:"roles"`
	IsAdmin  bool     `json:"is_admin"`
	Disabled bool     `json:"disabled"`
}

// List 列出用户。非 superadmin 只能看到本租户用户。
func (s *Service) List(ctx context.Context, actor *auth.User) ([]UserInfo, error) {
	q := `SELECT id, username, COALESCE(display_name,''), COALESCE(tenant_id,1),
	             COALESCE(roles, ARRAY['user']::text[]), is_admin, disabled
	      FROM local_users`
	var args []any
	if actor == nil || !actor.IsSuperAdmin() {
		tid := int64(-1) // fail-close：身份缺失时返回空集
		if actor != nil {
			tid = actor.TenantID
		}
		q += ` WHERE COALESCE(tenant_id,1) = $1`
		args = append(args, tid)
	}
	q += ` ORDER BY username`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserInfo{}
	for rows.Next() {
		var u UserInfo
		var roles pq.StringArray
		if err := rows.Scan(&u.ID, &u.Username, &u.Display, &u.TenantID, &roles,
			&u.IsAdmin, &u.Disabled); err != nil {
			return nil, err
		}
		u.Roles = []string(roles)
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateRequest 创建用户入参。
type CreateRequest struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	Display  string   `json:"display_name"`
	TenantID int64    `json:"tenant_id"`
	Roles    []string `json:"roles"`
}

// Create 创建用户。
//
// 权限：非 superadmin 只能在自己租户下创建，且不能授予 superadmin，
// 否则普通租户管理员可自我提权到跨租户。
func (s *Service) Create(ctx context.Context, actor *auth.User, req CreateRequest) (int64, error) {
	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		return 0, fmt.Errorf("username 与 password 必填")
	}
	roles, err := normalizeRoles(req.Roles)
	if err != nil {
		return 0, err
	}
	tenantID, err := s.resolveTarget(actor, req.TenantID, roles)
	if err != nil {
		return 0, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	isAdmin := containsRole(roles, auth.RoleAdmin) || containsRole(roles, auth.RoleSuperAdmin)

	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO local_users (username, password_hash, display_name, is_admin, tenant_id, roles)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		req.Username, string(hash), req.Display, isAdmin, tenantID, pq.Array(roles)).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SetTenantRoles 调整用户的租户与角色。
func (s *Service) SetTenantRoles(ctx context.Context, actor *auth.User, username string, tenantID int64, roles []string) error {
	norm, err := normalizeRoles(roles)
	if err != nil {
		return err
	}
	target, err := s.resolveTarget(actor, tenantID, norm)
	if err != nil {
		return err
	}
	// 非 superadmin 还须确认「被改对象」原本就在自己租户内，防止跨租户接管账号。
	if actor == nil || !actor.IsSuperAdmin() {
		var cur int64
		err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(tenant_id,1) FROM local_users WHERE username=$1`, username).Scan(&cur)
		if err == sql.ErrNoRows {
			return fmt.Errorf("用户不存在")
		}
		if err != nil {
			return err
		}
		if actor == nil || cur != actor.TenantID {
			return fmt.Errorf("无权修改其他租户的用户")
		}
	}
	isAdmin := containsRole(norm, auth.RoleAdmin) || containsRole(norm, auth.RoleSuperAdmin)
	res, err := s.db.ExecContext(ctx,
		`UPDATE local_users SET tenant_id=$2, roles=$3, is_admin=$4, updated_at=NOW()
		 WHERE username=$1`,
		username, target, pq.Array(norm), isAdmin)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("用户不存在")
	}
	return nil
}

// SetDisabled 启用/禁用用户（不删除，保留审计线索）。
func (s *Service) SetDisabled(ctx context.Context, actor *auth.User, username string, disabled bool) error {
	if actor != nil && actor.Username == username && disabled {
		return fmt.Errorf("不能禁用当前登录账号")
	}
	q := `UPDATE local_users SET disabled=$2, updated_at=NOW() WHERE username=$1`
	args := []any{username, disabled}
	if actor == nil || !actor.IsSuperAdmin() {
		tid := int64(-1)
		if actor != nil {
			tid = actor.TenantID
		}
		q += ` AND COALESCE(tenant_id,1)=$3`
		args = append(args, tid)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("用户不存在或无权操作")
	}
	return nil
}

// resolveTarget 校验目标租户与角色是否在 actor 权限范围内。
func (s *Service) resolveTarget(actor *auth.User, wantTenant int64, roles []string) (int64, error) {
	if actor != nil && actor.IsSuperAdmin() {
		if wantTenant <= 0 {
			wantTenant = actor.TenantID
		}
		if err := s.ensureTenantExists(wantTenant); err != nil {
			return 0, err
		}
		return wantTenant, nil
	}
	// 非 superadmin：禁止授予 superadmin（防自我提权）
	if containsRole(roles, auth.RoleSuperAdmin) {
		return 0, fmt.Errorf("无权授予 superadmin 角色")
	}
	if actor == nil {
		return 0, fmt.Errorf("未认证")
	}
	// 非 superadmin：只能操作自己租户
	if wantTenant > 0 && wantTenant != actor.TenantID {
		return 0, fmt.Errorf("无权操作租户 %d", wantTenant)
	}
	return actor.TenantID, nil
}

func (s *Service) ensureTenantExists(id int64) error {
	var exists bool
	if err := s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("租户 %s 不存在", strconv.FormatInt(id, 10))
	}
	return nil
}

func containsRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}
