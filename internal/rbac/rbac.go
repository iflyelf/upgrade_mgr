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
	// 与修改密码一致：至少 8 位、最多 72 字节，避免管理员创建弱密码账号。
	if err := auth.ValidatePassword("", req.Password); err != nil {
		return 0, err
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
	targetTenant, targetRoles, err := s.targetAccount(ctx, username)
	if err != nil {
		return err
	}
	target, err := s.resolveTarget(actor, tenantID, norm)
	if err != nil {
		return err
	}
	// 非 superadmin 还须确认「被改对象」原本就在自己租户内，防止跨租户接管账号；
	// 且不能修改管理员/超级管理员账号，否则可把同租户的 superadmin 降级。
	nonSuper := actor == nil || !actor.IsSuperAdmin()
	if nonSuper {
		if actor == nil || targetTenant != actor.TenantID {
			return fmt.Errorf("无权修改其他租户的用户")
		}
		if hasAdminRole(targetRoles) {
			return fmt.Errorf("仅 superadmin 可修改管理员账号")
		}
	}
	isAdmin := containsRole(norm, auth.RoleAdmin) || containsRole(norm, auth.RoleSuperAdmin)
	q := `UPDATE local_users SET tenant_id=$2, roles=$3, is_admin=$4, updated_at=NOW()
		WHERE username=$1`
	args := []any{username, target, pq.Array(norm), isAdmin}
	if nonSuper {
		// 复查条件写进 UPDATE，防止校验后目标角色被并发提升。
		q += ` AND COALESCE(tenant_id,1)=$5
		   AND NOT (COALESCE(roles, ARRAY['user']::text[]) && ARRAY['admin','superadmin']::text[])`
		args = append(args, actor.TenantID)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("用户不存在或无权修改管理员账号")
	}
	return nil
}

// targetAccount 返回用户所属租户与当前角色。
func (s *Service) targetAccount(ctx context.Context, username string) (int64, []string, error) {
	var tenantID int64
	var roles pq.StringArray
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(tenant_id,1), COALESCE(roles, ARRAY['user']::text[])
		   FROM local_users WHERE username=$1`, username).Scan(&tenantID, &roles)
	if err == sql.ErrNoRows {
		return 0, nil, fmt.Errorf("用户不存在")
	}
	if err != nil {
		return 0, nil, err
	}
	return tenantID, []string(roles), nil
}

// hasAdminRole 判断角色集合是否包含管理员或超级管理员。
func hasAdminRole(roles []string) bool {
	return containsRole(roles, auth.RoleAdmin) || containsRole(roles, auth.RoleSuperAdmin)
}

// SetDisabled 启用/禁用用户（不删除，保留审计线索）。
func (s *Service) SetDisabled(ctx context.Context, actor *auth.User, username string, disabled bool) error {
	if actor != nil && actor.Username == username && disabled {
		return fmt.Errorf("不能禁用当前登录账号")
	}
	q := `UPDATE local_users SET disabled=$2, updated_at=NOW() WHERE username=$1`
	args := []any{username, disabled}
	nonSuper := actor == nil || !actor.IsSuperAdmin()
	if nonSuper {
		tid := int64(-1)
		if actor != nil {
			tid = actor.TenantID
		}
		targetTenant, targetRoles, err := s.targetAccount(ctx, username)
		if err != nil {
			return err
		}
		if actor == nil || targetTenant != tid {
			return fmt.Errorf("用户不存在或无权操作")
		}
		// 租户管理员不能禁用/启用管理员或 superadmin（否则可借此接管/瘫痪租户）。
		if hasAdminRole(targetRoles) {
			return fmt.Errorf("仅 superadmin 可修改管理员账号")
		}
		// 复查条件写进 UPDATE，防止校验后目标被并发提升为管理员。
		q += ` AND COALESCE(tenant_id,1)=$3
		   AND NOT (COALESCE(roles, ARRAY['user']::text[]) && ARRAY['admin','superadmin']::text[])`
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

// Delete 删除用户（管理员）。
//
// 约束：
//   - 不能删除当前登录账号；
//   - 非 superadmin 只能删除本租户、且不能删除 admin/superadmin 账号；
//   - 清理团队成员关系与扩展字段值，审计日志保留（仅记录 username）。
func (s *Service) Delete(ctx context.Context, actor *auth.User, username string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username 必填")
	}
	if actor != nil && actor.Username == username {
		return fmt.Errorf("不能删除当前登录账号")
	}
	var tenantID int64
	var roles pq.StringArray
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(tenant_id,1), COALESCE(roles, ARRAY['user']::text[])
		   FROM local_users WHERE username=$1`, username).Scan(&tenantID, &roles)
	if err == sql.ErrNoRows {
		return fmt.Errorf("用户不存在")
	}
	if err != nil {
		return err
	}
	if actor == nil || !actor.IsSuperAdmin() {
		tid := int64(-1)
		if actor != nil {
			tid = actor.TenantID
		}
		if tenantID != tid {
			return fmt.Errorf("无权删除其他租户的用户")
		}
		for _, r := range roles {
			if r == auth.RoleAdmin || r == auth.RoleSuperAdmin {
				return fmt.Errorf("仅 superadmin 可删除管理员账号")
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM team_members WHERE user_id=$1`, username); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_field_values WHERE user_id=$1`, username); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM local_users WHERE username=$1`, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("用户不存在")
	}
	return tx.Commit()
}

// ResetPassword 管理员重置指定用户密码（无需旧密码）。
//
// 非 superadmin 仅能重置本租户普通用户；不能重置其他管理员密码。
// updated_at 会更新，从而让该用户旧会话立即失效。
func (s *Service) ResetPassword(ctx context.Context, actor *auth.User, username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username 必填")
	}
	if err := auth.ValidatePassword("", password); err != nil {
		return err
	}
	var tenantID int64
	var roles pq.StringArray
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(tenant_id,1), COALESCE(roles, ARRAY['user']::text[])
		   FROM local_users WHERE username=$1`, username).Scan(&tenantID, &roles)
	if err == sql.ErrNoRows {
		return fmt.Errorf("用户不存在")
	}
	if err != nil {
		return err
	}
	if actor == nil || !actor.IsSuperAdmin() {
		tid := int64(-1)
		if actor != nil {
			tid = actor.TenantID
		}
		if tenantID != tid {
			return fmt.Errorf("无权重置其他租户用户的密码")
		}
		for _, r := range roles {
			if r == auth.RoleAdmin || r == auth.RoleSuperAdmin {
				return fmt.Errorf("仅 superadmin 可重置管理员密码")
			}
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE local_users SET password_hash=$2, updated_at=NOW() WHERE username=$1`,
		username, string(hash))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("用户不存在")
	}
	return nil
}
