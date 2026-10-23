// Package org 人员组织：角色、团队、服务组与授权。
//
// 与 consul_mgr 的模型保持一致：
//   - 角色（roles）= 可复用的权限集合；
//   - 团队（teams）= 成员集合 + 对服务组的授权；
//   - 服务组（service_groups）= 一组组件；团队按服务组授权 read/write/delete/deploy；
//   - 授权可直接给权限，也可引用角色（取并集）。
//
// 与 consul_mgr 的差异：upgrade_mgr 没有 Consul 连接，服务组只持有组件集合。
package org

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// 权限常量。
const (
	PermRead   = "read"
	PermWrite  = "write"
	PermDelete = "delete"
	PermDeploy = "deploy"
)

// validPerms 允许的权限集合。
var validPerms = map[string]bool{
	PermRead: true, PermWrite: true, PermDelete: true, PermDeploy: true,
}

// Service 组织管理服务。
type Service struct{ db *sql.DB }

// NewService 创建服务。
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Role 角色。
type Role struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Code        string   `json:"code"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	// TeamCount 引用该角色的团队授权数量（删除前提示）。
	TeamCount int `json:"team_count"`
}

// Team 团队。
type Team struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	Description string `json:"description"`
	Status      int    `json:"status"`
	// OwnerOnly 任务操作策略：inherit（跟随全局）/ on / off。
	OwnerOnly   string `json:"owner_only"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	MemberCount int    `json:"member_count"`
	GroupCount  int    `json:"group_count"`
}

// Member 团队成员。
type Member struct {
	ID          int64  `json:"id"`
	TeamID      int64  `json:"team_id"`
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// Group 服务组。
type Group struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Code         string   `json:"code"`
	Description  string   `json:"description"`
	Status       int      `json:"status"`
	CreatedBy    string   `json:"created_by"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	ComponentIDs []int64  `json:"component_ids"`
	Components   []string `json:"components"` // 组件名（展示用）
	TeamCount    int      `json:"team_count"`
}

// GroupPermission 团队对服务组的授权。
//
// Services 限定可操作的组件（"*" 表示组内全部）；
// HostGroups 限定可操作的主机业务分组（如 app_code/biz_group 值，空表示不限）。
type GroupPermission struct {
	ID          int64    `json:"id"`
	TeamID      int64    `json:"team_id"`
	GroupID     int64    `json:"group_id"`
	GroupName   string   `json:"group_name"`
	Permissions []string `json:"permissions"`
	RoleIDs     []int64  `json:"role_ids"`
	Services    []string `json:"services"`
	HostGroups  []string `json:"host_groups"`
	CreatedAt   string   `json:"created_at"`
}

// ---- 校验 ----

// normalizePerms 校验并去重权限。
func normalizePerms(perms []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !validPerms[p] {
			return nil, fmt.Errorf("非法权限 %q（允许 read/write/delete/deploy）", p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

func trimName(s string) string { return strings.TrimSpace(s) }

// ---- 角色 ----

// ListRoles 列出角色。
func (s *Service) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.name, COALESCE(r.code,''), COALESCE(r.description,''),
		       COALESCE(r.permissions,'{}'),
		       TO_CHAR(r.created_at,'YYYY-MM-DD HH24:MI:SS'),
		       TO_CHAR(r.updated_at,'YYYY-MM-DD HH24:MI:SS'),
		       (SELECT COUNT(*) FROM team_group_permissions p WHERE r.id = ANY(p.role_ids))
		FROM roles r ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		var r Role
		var perms pq.StringArray
		if err := rows.Scan(&r.ID, &r.Name, &r.Code, &r.Description, &perms,
			&r.CreatedAt, &r.UpdatedAt, &r.TeamCount); err != nil {
			return nil, err
		}
		r.Permissions = []string(perms)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateRole 创建角色。
func (s *Service) CreateRole(ctx context.Context, name, code, description string, perms []string) (int64, error) {
	name = trimName(name)
	if name == "" {
		return 0, fmt.Errorf("角色名称不能为空")
	}
	if code = trimName(code); code == "" {
		code = name
	}
	p, err := normalizePerms(perms)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO roles (name, code, description, permissions)
		VALUES ($1,$2,$3,$4) RETURNING id`,
		name, code, trimName(description), pq.Array(p)).Scan(&id)
	if err != nil {
		if isUnique(err) {
			return 0, fmt.Errorf("角色名称「%s」已存在", name)
		}
		return 0, err
	}
	return id, nil
}

// UpdateRole 更新角色。
func (s *Service) UpdateRole(ctx context.Context, id int64, name, description string, perms []string) error {
	name = trimName(name)
	if name == "" {
		return fmt.Errorf("角色名称不能为空")
	}
	p, err := normalizePerms(perms)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE roles SET name=$2, description=$3, permissions=$4, updated_at=NOW()
		WHERE id=$1`, id, name, trimName(description), pq.Array(p))
	if err != nil {
		if isUnique(err) {
			return fmt.Errorf("角色名称「%s」已存在", name)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("角色不存在")
	}
	return nil
}

// DeleteRole 删除角色；被团队授权引用时拒绝（避免静默丢权限）。
func (s *Service) DeleteRole(ctx context.Context, id int64) error {
	var refs int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM team_group_permissions WHERE $1 = ANY(role_ids)`, id).Scan(&refs); err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("角色仍被 %d 个团队授权引用，请先解除引用", refs)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM roles WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("角色不存在")
	}
	return nil
}

// ---- 团队 ----

// ListTeams 列出团队。
func (s *Service) ListTeams(ctx context.Context) ([]Team, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name, COALESCE(t.code,''), COALESCE(t.description,''), t.status,
		       COALESCE(t.owner_only,'inherit'),
		       COALESCE(t.created_by,''),
		       TO_CHAR(t.created_at,'YYYY-MM-DD HH24:MI:SS'),
		       TO_CHAR(t.updated_at,'YYYY-MM-DD HH24:MI:SS'),
		       (SELECT COUNT(*) FROM team_members m WHERE m.team_id=t.id),
		       (SELECT COUNT(*) FROM team_group_permissions g WHERE g.team_id=t.id)
		FROM teams t ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.Code, &t.Description, &t.Status,
			&t.OwnerOnly, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt, &t.MemberCount, &t.GroupCount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTeam 创建团队。
func (s *Service) CreateTeam(ctx context.Context, name, code, description string, members []string, createdBy string) (int64, error) {
	return s.CreateTeamWithPolicy(ctx, name, code, description, members, createdBy, teamOwnerInherit)
}

// 团队任务操作策略取值。
const (
	teamOwnerInherit = "inherit"
	teamOwnerOn      = "on"
	teamOwnerOff     = "off"
)

// NormalizeTeamOwnerOnly 校验并规范化策略值（空/未知 → inherit）。
func NormalizeTeamOwnerOnly(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case teamOwnerOn, "true", "1":
		return teamOwnerOn
	case teamOwnerOff, "false", "0":
		return teamOwnerOff
	default:
		return teamOwnerInherit
	}
}

// CreateTeamWithPolicy 创建团队并指定任务操作策略。
func (s *Service) CreateTeamWithPolicy(ctx context.Context, name, code, description string,
	members []string, createdBy, ownerOnly string) (int64, error) {
	name = trimName(name)
	if name == "" {
		return 0, fmt.Errorf("团队名称不能为空")
	}
	if code = trimName(code); code == "" {
		code = name
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO teams (name, code, description, created_by, owner_only)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		name, code, trimName(description), createdBy, NormalizeTeamOwnerOnly(ownerOnly)).Scan(&id); err != nil {
		if isUnique(err) {
			return 0, fmt.Errorf("团队名称「%s」已存在", name)
		}
		return 0, err
	}
	for _, m := range members {
		m = trimName(m)
		if m == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_members (team_id, user_id, username)
			VALUES ($1,$2,$2) ON CONFLICT (team_id, user_id) DO NOTHING`, id, m); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// UpdateTeam 更新团队基本信息。
func (s *Service) UpdateTeam(ctx context.Context, id int64, name, description string, status int) error {
	return s.UpdateTeamWithPolicy(ctx, id, name, description, status, "")
}

// UpdateTeamWithPolicy 更新团队（ownerOnly 为空表示不修改）。
func (s *Service) UpdateTeamWithPolicy(ctx context.Context, id int64, name, description string,
	status int, ownerOnly string) error {
	name = trimName(name)
	if name == "" {
		return fmt.Errorf("团队名称不能为空")
	}
	if status != 0 {
		status = 1
	}
	policy := NormalizeTeamOwnerOnly(ownerOnly)
	if strings.TrimSpace(ownerOnly) == "" {
		policy = "" // 不修改
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE teams SET name=$2, description=$3, status=$4,
		       owner_only = COALESCE(NULLIF($5,''), owner_only), updated_at=NOW()
		WHERE id=$1`,
		id, name, trimName(description), status, policy)
	if err != nil {
		if isUnique(err) {
			return fmt.Errorf("团队名称「%s」已存在", name)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("团队不存在")
	}
	return nil
}

// DeleteTeam 删除团队（成员与授权级联删除）。
func (s *Service) DeleteTeam(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM teams WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("团队不存在")
	}
	return nil
}

// ListMembers 列出团队成员。
func (s *Service) ListMembers(ctx context.Context, teamID int64) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.team_id, m.user_id, COALESCE(m.username,''), COALESCE(m.display_name,''),
		       TO_CHAR(m.created_at,'YYYY-MM-DD HH24:MI:SS')
		FROM team_members m WHERE m.team_id=$1 ORDER BY m.id`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.TeamID, &m.UserID, &m.Username, &m.DisplayName, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMembers 批量添加成员（按用户名，忽略重复）。
func (s *Service) AddMembers(ctx context.Context, teamID int64, usernames []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, u := range usernames {
		u = trimName(u)
		if u == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_members (team_id, user_id, username)
			VALUES ($1,$2,$2) ON CONFLICT (team_id, user_id) DO NOTHING`, teamID, u); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RemoveMember 移除成员。
func (s *Service) RemoveMember(ctx context.Context, teamID int64, userID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM team_members WHERE team_id=$1 AND user_id=$2`, teamID, trimName(userID))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("成员不存在")
	}
	return nil
}

// ---- 服务组 ----

// ListGroups 列出服务组（含组件）。
func (s *Service) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.id, g.name, COALESCE(g.code,''), COALESCE(g.description,''), g.status,
		       COALESCE(g.created_by,''),
		       TO_CHAR(g.created_at,'YYYY-MM-DD HH24:MI:SS'),
		       TO_CHAR(g.updated_at,'YYYY-MM-DD HH24:MI:SS'),
		       (SELECT COUNT(*) FROM team_group_permissions p WHERE p.group_id=g.id)
		FROM service_groups g ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Code, &g.Description, &g.Status,
			&g.CreatedBy, &g.CreatedAt, &g.UpdatedAt, &g.TeamCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 逐个补齐组件（组数量通常很少）。
	for i := range out {
		ids, names, err := s.groupComponents(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].ComponentIDs, out[i].Components = ids, names
	}
	return out, nil
}

// groupComponents 返回组内组件 ID 与名称。
func (s *Service) groupComponents(ctx context.Context, groupID int64) ([]int64, []string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name FROM service_group_components sg
		JOIN components c ON c.id = sg.component_id
		WHERE sg.group_id=$1 ORDER BY c.name`, groupID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	ids := []int64{}
	names := []string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err == nil {
			ids = append(ids, id)
			names = append(names, name)
		}
	}
	return ids, names, rows.Err()
}

// CreateGroup 创建服务组。
func (s *Service) CreateGroup(ctx context.Context, name, code, description string, componentIDs []int64, createdBy string) (int64, error) {
	name = trimName(name)
	if name == "" {
		return 0, fmt.Errorf("服务组名称不能为空")
	}
	if code = trimName(code); code == "" {
		code = name
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO service_groups (name, code, description, created_by)
		VALUES ($1,$2,$3,$4) RETURNING id`,
		name, code, trimName(description), createdBy).Scan(&id); err != nil {
		if isUnique(err) {
			return 0, fmt.Errorf("服务组名称「%s」已存在", name)
		}
		return 0, err
	}
	if err := setGroupComponentsTx(ctx, tx, id, componentIDs); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateGroup 更新服务组（含组件集合）。
func (s *Service) UpdateGroup(ctx context.Context, id int64, name, description string, componentIDs []int64) error {
	name = trimName(name)
	if name == "" {
		return fmt.Errorf("服务组名称不能为空")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE service_groups SET name=$2, description=$3, updated_at=NOW() WHERE id=$1`,
		id, name, trimName(description))
	if err != nil {
		if isUnique(err) {
			return fmt.Errorf("服务组名称「%s」已存在", name)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("服务组不存在")
	}
	if err := setGroupComponentsTx(ctx, tx, id, componentIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// setGroupComponentsTx 覆盖式设置组内组件。
func setGroupComponentsTx(ctx context.Context, tx *sql.Tx, groupID int64, componentIDs []int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM service_group_components WHERE group_id=$1`, groupID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, cid := range componentIDs {
		if cid <= 0 || seen[cid] {
			continue
		}
		seen[cid] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO service_group_components (group_id, component_id)
			VALUES ($1,$2) ON CONFLICT DO NOTHING`, groupID, cid); err != nil {
			return err
		}
	}
	return nil
}

// DeleteGroup 删除服务组（授权级联删除）。
func (s *Service) DeleteGroup(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM service_groups WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("服务组不存在")
	}
	return nil
}

// ---- 团队授权 ----

// ListGroupPermissions 列出团队在各服务组的授权。
func (s *Service) ListGroupPermissions(ctx context.Context, teamID int64) ([]GroupPermission, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.team_id, p.group_id, COALESCE(g.name,''),
		       COALESCE(p.permissions,'{}'), COALESCE(p.role_ids,'{}'), COALESCE(p.services,'{}'),
		       COALESCE(p.host_groups,'{}'),
		       TO_CHAR(p.created_at,'YYYY-MM-DD HH24:MI:SS')
		FROM team_group_permissions p
		LEFT JOIN service_groups g ON g.id = p.group_id
		WHERE p.team_id=$1 ORDER BY p.group_id`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupPermission{}
	for rows.Next() {
		var gp GroupPermission
		var perms, services, hostGroups pq.StringArray
		var roleIDs pq.Int64Array
		if err := rows.Scan(&gp.ID, &gp.TeamID, &gp.GroupID, &gp.GroupName,
			&perms, &roleIDs, &services, &hostGroups, &gp.CreatedAt); err != nil {
			return nil, err
		}
		gp.Permissions = []string(perms)
		gp.RoleIDs = []int64(roleIDs)
		gp.Services = []string(services)
		gp.HostGroups = []string(hostGroups)
		out = append(out, gp)
	}
	return out, rows.Err()
}

// SetGroupPermission 设置团队对某服务组的授权（存在则覆盖）。
func (s *Service) SetGroupPermission(ctx context.Context, teamID, groupID int64, perms []string, roleIDs []int64, services, hostGroups []string) error {
	p, err := normalizePerms(perms)
	if err != nil {
		return err
	}
	// 校验角色存在且服务名在组内（允许 "*"）。
	if len(roleIDs) > 0 {
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM roles WHERE id = ANY($1)`, pq.Array(roleIDs)).Scan(&n); err != nil {
			return err
		}
		if n != len(uniqueInt64(roleIDs)) {
			return fmt.Errorf("存在无效的角色 ID")
		}
	}
	validServices, err := s.groupServiceNames(ctx, groupID)
	if err != nil {
		return err
	}
	cleaned := make([]string, 0, len(services))
	for _, svc := range services {
		svc = trimName(svc)
		if svc == "" {
			continue
		}
		if svc == "*" || validServices[svc] {
			cleaned = append(cleaned, svc)
		}
	}
	cleanedHosts := make([]string, 0, len(hostGroups))
	for _, hg := range hostGroups {
		if hg = trimName(hg); hg != "" {
			cleanedHosts = append(cleanedHosts, hg)
		}
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO team_group_permissions (team_id, group_id, permissions, role_ids, services, host_groups)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (team_id, group_id) DO UPDATE SET
			permissions = EXCLUDED.permissions,
			role_ids    = EXCLUDED.role_ids,
			services    = EXCLUDED.services,
			host_groups = EXCLUDED.host_groups,
			updated_at  = NOW()`,
		teamID, groupID, pq.Array(p), pq.Array(roleIDs), pq.Array(cleaned), pq.Array(cleanedHosts))
	return err
}

// DeleteGroupPermission 解除授权。
func (s *Service) DeleteGroupPermission(ctx context.Context, teamID, groupID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM team_group_permissions WHERE team_id=$1 AND group_id=$2`, teamID, groupID)
	return err
}

// groupServiceNames 返回组内组件名集合。
func (s *Service) groupServiceNames(ctx context.Context, groupID int64) (map[string]bool, error) {
	_, names, err := s.groupComponents(ctx, groupID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out, nil
}

// EffectivePermissions 计算某用户通过团队获得的全部权限（团队并集）。
//
// 返回：
//   - perms: map[componentName]map[perm]bool
//   - hostGroups: map[componentName]map[hostGroup]bool（空集合 = 不限主机分组）
func (s *Service) EffectivePermissions(ctx context.Context, username string) (map[string]map[string]bool, error) {
	perms, _, err := s.EffectivePermissionsWithHosts(ctx, username)
	return perms, err
}

// EffectivePermissionsWithHosts 同 EffectivePermissions，同时返回主机分组范围。
//
// 主机分组取 hosts 表的 app_code / biz_group（团队授权里按值填写）。
// 空集合表示该组件不限制主机范围。
func (s *Service) EffectivePermissionsWithHosts(ctx context.Context, username string) (map[string]map[string]bool, map[string]map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.permissions, COALESCE(p.role_ids,'{}'), COALESCE(p.services,'{}'),
		       COALESCE(p.host_groups,'{}'),
		       sg.component_id, c.name
		FROM team_members m
		JOIN team_group_permissions p ON p.team_id = m.team_id
		JOIN service_group_components sg ON sg.group_id = p.group_id
		JOIN components c ON c.id = sg.component_id
		WHERE m.user_id = $1`, username)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	// 先取角色权限，避免在结果集循环中再查询（同一连接上的嵌套查询易出错）。
	rolePerms, err := s.rolePermissions(ctx)
	if err != nil {
		return nil, nil, err
	}

	permsOut := map[string]map[string]bool{}
	hostsOut := map[string]map[string]bool{}
	for rows.Next() {
		var perms, services, hostGroups pq.StringArray
		var roleIDs pq.Int64Array
		var compID int64
		var compName string
		if err := rows.Scan(&perms, &roleIDs, &services, &hostGroups, &compID, &compName); err != nil {
			return nil, nil, err
		}
		// 服务维度：空 services 表示未授权任何服务；"*" 表示组内全部组件。
		allowed := false
		for _, svc := range services {
			if svc == "*" || svc == compName {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		if permsOut[compName] == nil {
			permsOut[compName] = map[string]bool{}
		}
		for _, p := range perms {
			permsOut[compName][p] = true
		}
		for _, rid := range roleIDs {
			for _, p := range rolePerms[rid] {
				permsOut[compName][p] = true
			}
		}
		// 主机分组：多个团队授权取并集；任一条目为空表示不限。
		if hostsOut[compName] == nil {
			hostsOut[compName] = map[string]bool{}
		}
		if len(hostGroups) == 0 {
			// 空表示不限主机；用特殊标记表示「全部」，避免被并集逻辑覆盖成具体值。
			hostsOut[compName]["*"] = true
		} else {
			for _, hg := range hostGroups {
				hostsOut[compName][hg] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// 规范化：含 "*" 时只保留 "*"，避免调用方还要判断两种形态。
	for compName, set := range hostsOut {
		if set["*"] {
			hostsOut[compName] = map[string]bool{"*": true}
		}
	}
	return permsOut, hostsOut, nil
}

// rolePermissions 返回 roleID → 权限集合。
func (s *Service) rolePermissions(ctx context.Context) (map[int64][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(permissions,'{}') FROM roles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var perms pq.StringArray
		if err := rows.Scan(&id, &perms); err == nil {
			out[id] = []string(perms)
		}
	}
	return out, rows.Err()
}

// TeamOwnerPolicy 返回该用户所属团队的任务操作策略（去重后的集合）。
//
// 返回 "" 表示未加入任何团队（调用方回退全局设置）。
func (s *Service) TeamOwnerPolicy(ctx context.Context, username string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT COALESCE(t.owner_only,'inherit')
		FROM team_members m JOIN teams t ON t.id = m.team_id
		WHERE m.user_id = $1`, strings.TrimSpace(username))
	if err != nil {
		return "", err
	}
	defer rows.Close()

	hasOn, hasOff, hasInherit := false, false, false
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return "", err
		}
		switch NormalizeTeamOwnerOnly(v) {
		case teamOwnerOn:
			hasOn = true
		case teamOwnerOff:
			hasOff = true
		default:
			hasInherit = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	// 任一团队要求 on → 受限（更严格优先，避免绕过）。
	if hasOn {
		return teamOwnerOn, nil
	}
	if hasOff && !hasInherit {
		return teamOwnerOff, nil
	}
	if hasInherit || hasOff {
		return teamOwnerInherit, nil
	}
	return "", nil
}

// CanShareTasks 判断 actor 能否操作 creator 创建的任务。
//
// 规则：二者同属至少一个 owner_only='off' 的团队时允许。
// 仅当 actor 的生效策略为 off 时由任务服务调用。
func (s *Service) CanShareTasks(ctx context.Context, actor, creator string) (bool, error) {
	actor = strings.TrimSpace(actor)
	creator = strings.TrimSpace(creator)
	if actor == "" || creator == "" {
		return false, nil
	}
	if actor == creator {
		return true, nil
	}
	var ok bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM team_members a
			  JOIN teams t ON t.id = a.team_id
			  JOIN team_members b ON b.team_id = a.team_id
			 WHERE a.user_id = $1 AND b.user_id = $2 AND t.owner_only = 'off'
		)`, actor, creator).Scan(&ok)
	return ok, err
}

// TeamMates 返回与该用户同属至少一个团队的其他用户名集合（不含自己）。
func (s *Service) TeamMates(ctx context.Context, username string) (map[string]bool, error) {
	out := map[string]bool{}
	username = strings.TrimSpace(username)
	if username == "" {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT b.user_id
		  FROM team_members a
		  JOIN team_members b ON b.team_id = a.team_id
		 WHERE a.user_id = $1 AND b.user_id <> $1`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out[u] = true
	}
	return out, rows.Err()
}

// SharesTeam 判断两个用户是否同属至少一个团队。
//
// 与团队 owner 策略无关：只要在同一团队即可见任务详情/日志。
func (s *Service) SharesTeam(ctx context.Context, a, b string) (bool, error) {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false, nil
	}
	if a == b {
		return true, nil
	}
	var ok bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM team_members x
			  JOIN team_members y ON y.team_id = x.team_id
			 WHERE x.user_id = $1 AND y.user_id = $2
		)`, a, b).Scan(&ok)
	return ok, err
}

// ---- 辅助 ----

func isUnique(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "duplicate key") ||
		strings.Contains(err.Error(), "23505"))
}

func uniqueInt64(in []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
