// Package aitoken AI 访问令牌：创建、列表、注销与 Bearer 认证。
//
// 令牌只保存 SHA-256 哈希，明文仅在创建时返回一次。令牌继承创建者身份：
// builtin 用户每次认证按 local_users 实时取角色/租户/禁用状态（删除或禁用后立即失效），
// SSO 用户（不在 local_users）使用创建时的角色快照。
package aitoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// 随机部分字节数（hex 后长度翻倍）；展示前缀额外保留的随机字符数。
const (
	randomBytes      = 32
	prefixRandomKeep = 8
	maxNameBytes     = 100
)

// ErrInvalid 令牌无效（不存在 / 已注销 / 已过期 / 用户被禁用）。对外统一提示，避免枚举。
var ErrInvalid = errors.New("AI 访问令牌无效、已过期或已注销")

// Token 令牌视图（不含明文与哈希）。
type Token struct {
	ID          int64      `json:"id"`
	TenantID    int64      `json:"tenant_id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Provider    string     `json:"auth_provider"`
	IsAdmin     bool       `json:"is_admin"`
	Roles       []string   `json:"roles"`
	Name        string     `json:"name"`
	Prefix      string     `json:"token_prefix"`
	AllowWrite  bool       `json:"allow_write"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	RevokedBy   string     `json:"revoked_by"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	LastUsedIP  string     `json:"last_used_ip"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	Status      string     `json:"status"`
}

// Policy 令牌创建策略（由调用方从设置快照构造，热生效）。
type Policy struct {
	Prefix         string
	MinRole        string
	DefaultTTL     time.Duration
	MaxTTL         time.Duration // 0 = 不限制
	AllowPermanent bool
	MaxPerUser     int
}

// CreateInput 创建参数。ExpiresIn 为空时使用默认有效期；Permanent=true 表示永不过期。
type CreateInput struct {
	Name       string `json:"name"`
	AllowWrite bool   `json:"allow_write"`
	ExpiresIn  string `json:"expires_in"`
	Permanent  bool   `json:"permanent"`
}

// Created 创建结果：Plaintext 仅此一次返回。
type Created struct {
	Token
	Plaintext string `json:"token"`
}

// Repo 令牌仓库。
type Repo struct {
	db        *sql.DB
	touchMu   sync.Mutex
	lastTouch map[int64]time.Time
}

// NewRepo 创建仓库。
func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db, lastTouch: map[int64]time.Time{}}
}

// Hash 计算令牌哈希。
func Hash(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}

func generate(prefix string) (string, error) {
	b := make([]byte, randomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机令牌失败: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

// displayPrefix 展示用前缀：配置前缀 + 少量随机字符，便于在列表中辨认。
func displayPrefix(prefix, plaintext string) string {
	n := len(prefix) + prefixRandomKeep
	if n > len(plaintext) {
		n = len(plaintext)
	}
	return plaintext[:n]
}

// roleRank 角色等级：user < admin < superadmin。
func roleRank(u *auth.User) int {
	switch {
	case u == nil:
		return -1
	case u.IsSuperAdmin():
		return 2
	case u.IsAdmin || u.HasRole(auth.RoleAdmin):
		return 1
	default:
		return 0
	}
}

func minRoleRank(role string) int {
	switch role {
	case auth.RoleSuperAdmin:
		return 2
	case auth.RoleAdmin:
		return 1
	default:
		return 0
	}
}

// CanCreate 判断用户是否满足创建令牌的最低角色。
func CanCreate(u *auth.User, minRole string) bool {
	return roleRank(u) >= minRoleRank(minRole)
}

func tenantOf(u *auth.User) int64 {
	if u == nil || u.TenantID <= 0 {
		return 1
	}
	return u.TenantID
}

// resolveExpiry 按策略计算到期时间；nil 表示永久。
func resolveExpiry(in CreateInput, p Policy, now time.Time) (*time.Time, error) {
	if in.Permanent || strings.EqualFold(strings.TrimSpace(in.ExpiresIn), "permanent") {
		if !p.AllowPermanent {
			return nil, fmt.Errorf("当前设置不允许创建永久令牌")
		}
		if p.MaxTTL > 0 {
			return nil, fmt.Errorf("已配置令牌最长有效期 %s，不能创建永久令牌", p.MaxTTL)
		}
		return nil, nil
	}
	ttl := p.DefaultTTL
	if s := strings.TrimSpace(in.ExpiresIn); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("有效期格式错误（示例 24h / 720h）")
		}
		ttl = d
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("令牌默认有效期未配置")
	}
	if p.MaxTTL > 0 && ttl > p.MaxTTL {
		return nil, fmt.Errorf("有效期超过上限 %s", p.MaxTTL)
	}
	t := now.Add(ttl)
	return &t, nil
}

func cleanName(s string) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len()+len(string(r)) > maxNameBytes {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Create 为当前用户创建令牌。provider 为当前认证方式（builtin / casdoor）。
func (r *Repo) Create(ctx context.Context, actor *auth.User, provider string, in CreateInput, p Policy) (*Created, error) {
	if actor == nil || strings.TrimSpace(actor.Username) == "" {
		return nil, fmt.Errorf("未认证")
	}
	if !CanCreate(actor, p.MinRole) {
		return nil, fmt.Errorf("创建 AI 令牌至少需要 %s 角色", p.MinRole)
	}
	expiresAt, err := resolveExpiry(in, p, time.Now())
	if err != nil {
		return nil, err
	}
	name := cleanName(in.Name)
	if name == "" {
		return nil, fmt.Errorf("请填写令牌名称")
	}
	tenantID := tenantOf(actor)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 同一用户串行创建，保证上限检查与插入原子（多副本同样有效）。
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('ai_token:' || $1))`, actor.Username); err != nil {
		return nil, err
	}
	if p.MaxPerUser > 0 {
		var cnt int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM ai_access_tokens
			WHERE username=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > NOW())`,
			actor.Username).Scan(&cnt); err != nil {
			return nil, err
		}
		if cnt >= p.MaxPerUser {
			return nil, fmt.Errorf("有效令牌已达上限 %d 个，请先注销不用的令牌", p.MaxPerUser)
		}
	}
	plaintext, err := generate(p.Prefix)
	if err != nil {
		return nil, err
	}
	roles := actor.Roles
	if len(roles) == 0 {
		roles = []string{auth.RoleUser}
	}
	out := &Created{Plaintext: plaintext}
	out.TenantID, out.Username, out.DisplayName = tenantID, actor.Username, actor.DisplayName
	out.Provider, out.IsAdmin, out.Roles = provider, actor.IsAdmin, roles
	out.Name, out.Prefix, out.AllowWrite, out.ExpiresAt = name, displayPrefix(p.Prefix, plaintext), in.AllowWrite, expiresAt
	out.CreatedBy, out.Status = actor.Username, "active"
	var exp any
	if expiresAt != nil {
		exp = *expiresAt
	}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO ai_access_tokens (tenant_id, username, display_name, auth_provider, is_admin, roles,
		                              name, token_hash, token_prefix, allow_write, expires_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id, created_at`,
		tenantID, actor.Username, actor.DisplayName, provider, actor.IsAdmin, pq.Array(roles),
		name, Hash(plaintext), out.Prefix, in.AllowWrite, exp, actor.Username,
	).Scan(&out.ID, &out.CreatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// visibility 返回列表/注销的可见范围条件（参数从 startIdx 开始编号）。
//
// 普通用户：仅本人；admin：本租户；superadmin：全部。
func visibility(actor *auth.User, startIdx int) (string, []any) {
	switch roleRank(actor) {
	case 2:
		return "TRUE", nil
	case 1:
		return fmt.Sprintf("tenant_id=$%d", startIdx), []any{tenantOf(actor)}
	default:
		return fmt.Sprintf("username=$%d", startIdx), []any{actor.Username}
	}
}

func statusOf(t *Token, now time.Time) string {
	switch {
	case t.RevokedAt != nil:
		return "revoked"
	case t.ExpiresAt != nil && !t.ExpiresAt.After(now):
		return "expired"
	default:
		return "active"
	}
}

// ListOptions 列表参数。Mine=true 时只看本人令牌（管理员也一样）。
type ListOptions struct {
	Mine bool
	Page int
	Size int
}

// List 分页列出可见令牌。
func (r *Repo) List(ctx context.Context, actor *auth.User, o ListOptions) ([]Token, int, error) {
	if actor == nil {
		return nil, 0, fmt.Errorf("未认证")
	}
	cond, args := visibility(actor, 1)
	if o.Mine {
		args = append(args, actor.Username)
		cond += fmt.Sprintf(" AND username=$%d", len(args))
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_access_tokens WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if o.Page < 1 {
		o.Page = 1
	}
	args = append(args, o.Size, (o.Page-1)*o.Size)
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, username, display_name, auth_provider, is_admin, roles, name, token_prefix,
		       allow_write, expires_at, revoked_at, revoked_by, last_used_at, last_used_ip, created_by, created_at
		FROM ai_access_tokens WHERE `+cond+`
		ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	now := time.Now()
	out := []Token{}
	for rows.Next() {
		var t Token
		var roles pq.StringArray
		var exp, rev, used sql.NullTime
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Username, &t.DisplayName, &t.Provider, &t.IsAdmin, &roles,
			&t.Name, &t.Prefix, &t.AllowWrite, &exp, &rev, &t.RevokedBy, &used, &t.LastUsedIP,
			&t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, 0, err
		}
		t.Roles = []string(roles)
		t.ExpiresAt, t.RevokedAt, t.LastUsedAt = nullTime(exp), nullTime(rev), nullTime(used)
		t.Status = statusOf(&t, now)
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// Revoke 注销令牌（本人 / 本租户管理员 / superadmin）。
func (r *Repo) Revoke(ctx context.Context, actor *auth.User, id int64) (*Token, error) {
	if actor == nil {
		return nil, fmt.Errorf("未认证")
	}
	cond, vargs := visibility(actor, 3)
	args := append([]any{id, actor.Username}, vargs...)
	var t Token
	err := r.db.QueryRowContext(ctx, `
		UPDATE ai_access_tokens
		SET revoked_at=COALESCE(revoked_at, NOW()),
		    revoked_by=CASE WHEN revoked_by='' THEN $2 ELSE revoked_by END
		WHERE id=$1 AND `+cond+`
		RETURNING id, username, name`, args...).Scan(&t.ID, &t.Username, &t.Name)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("令牌不存在或无权操作")
	}
	if err != nil {
		return nil, err
	}
	r.touchMu.Lock()
	delete(r.lastTouch, id)
	r.touchMu.Unlock()
	return &t, nil
}

// Authenticate 校验明文令牌，返回令牌身份对应的用户与令牌信息。
func (r *Repo) Authenticate(ctx context.Context, plaintext string) (*auth.User, *Token, error) {
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return nil, nil, ErrInvalid
	}
	var t Token
	var roles pq.StringArray
	var exp, rev sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, username, display_name, auth_provider, is_admin, roles, name,
		       token_prefix, allow_write, expires_at, revoked_at
		FROM ai_access_tokens WHERE token_hash=$1`, Hash(plaintext)).Scan(
		&t.ID, &t.TenantID, &t.Username, &t.DisplayName, &t.Provider, &t.IsAdmin, &roles,
		&t.Name, &t.Prefix, &t.AllowWrite, &exp, &rev)
	if err == sql.ErrNoRows {
		return nil, nil, ErrInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	t.Roles = []string(roles)
	t.ExpiresAt, t.RevokedAt = nullTime(exp), nullTime(rev)
	if t.Status = statusOf(&t, time.Now()); t.Status != "active" {
		return nil, nil, ErrInvalid
	}

	// 优先按 local_users 取实时身份：builtin 用户与已开通本地账号的 SSO 用户都适用。
	var (
		uid      int64
		display  string
		isAdmin  bool
		disabled bool
		tenantID int64
		uroles   pq.StringArray
	)
	err = r.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(display_name,''), is_admin, disabled, COALESCE(tenant_id,1),
		       COALESCE(roles, ARRAY['user']::text[])
		FROM local_users WHERE username=$1`, t.Username).Scan(&uid, &display, &isAdmin, &disabled, &tenantID, &uroles)
	switch {
	case err == nil:
		if disabled {
			return nil, nil, ErrInvalid
		}
		return &auth.User{
			ID: strconv.FormatInt(uid, 10), Username: t.Username, DisplayName: display,
			IsAdmin: isAdmin, TenantID: tenantID, Roles: []string(uroles),
		}, &t, nil
	case err == sql.ErrNoRows:
		// builtin 用户已被删除：令牌随之失效；SSO 用户使用创建时快照。
		if t.Provider == "builtin" {
			return nil, nil, ErrInvalid
		}
		return &auth.User{
			ID: t.Username, Username: t.Username, DisplayName: t.DisplayName,
			IsAdmin: t.IsAdmin, TenantID: t.TenantID, Roles: t.Roles,
		}, &t, nil
	default:
		return nil, nil, err
	}
}

// Touch 异步记录最近使用时间与来源 IP；同一令牌在 interval 内最多写一次库。
func (r *Repo) Touch(tokenID int64, ip string, interval time.Duration) {
	now := time.Now()
	r.touchMu.Lock()
	if last, ok := r.lastTouch[tokenID]; ok && now.Sub(last) < interval {
		r.touchMu.Unlock()
		return
	}
	r.lastTouch[tokenID] = now
	// 防止长期运行时 map 无限增长：清掉早已过期的记录。
	for id, ts := range r.lastTouch {
		if now.Sub(ts) > interval*2 && id != tokenID {
			delete(r.lastTouch, id)
		}
	}
	r.touchMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = r.db.ExecContext(ctx,
			`UPDATE ai_access_tokens SET last_used_at=NOW(), last_used_ip=$2 WHERE id=$1`, tokenID, ip)
	}()
}

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
