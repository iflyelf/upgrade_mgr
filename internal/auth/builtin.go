package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// Builtin 内置账号认证：bcrypt 校验 + HMAC 签名会话 cookie。
type Builtin struct {
	db *sql.DB
	sc *sessionCodec
	// 缓存：避免每次请求查库，30s TTL。键为 user_id。
	cache   map[string]*cachedUser
	cacheMu sync.RWMutex
	// dummyHash 用于用户不存在时的 dummy bcrypt 比较，防止时序侧信道泄露账号存在性。
	dummyHash     string
	dummyHashOnce sync.Once
	// lookupUser 可注入的用户查询函数，便于测试。
	lookupUser func(ctx context.Context, id string) (*cachedUser, error)
}

type cachedUser struct {
	Username   string
	Display    string
	IsAdmin    bool
	Disabled   bool
	TenantID   int64
	Roles      []string
	UpdatedAt  time.Time
	CachedAt   time.Time
	PasswordOK bool // 密码已在 Login 中校验，缓存命中时跳过
}

const userCacheTTL = 30 * time.Second

// NewBuiltin 创建内置认证；sessionSecret 为空则随机生成（重启后需重新登录）。
func NewBuiltin(db *sql.DB, sessionSecret string, secure bool) *Builtin {
	b := &Builtin{db: db, sc: newSessionCodec(sessionSecret, secure), cache: make(map[string]*cachedUser)}
	b.lookupUser = b.defaultLookup
	go b.evictCache()
	return b
}

// EnsureAdmin 确保内置管理员存在（首次启动种子）。
//
// password 为明文或 $2 开头哈希；为空时生成随机强密码并打印一次。
// 已存在账号时不覆盖（保护已初始化实例不被后续配置误修改）。
func (b *Builtin) EnsureAdmin(ctx context.Context, username, password string) error {
	if username == "" {
		username = "admin"
	}
	var count int
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM local_users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil // 已有账号，不覆盖
	}
	generated := ""
	if password == "" {
		buf := make([]byte, 18)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		generated = base64.RawURLEncoding.EncodeToString(buf)
		password = generated
	}
	hash, err := hashOrPassthrough(password)
	if err != nil {
		return err
	}
	// 首个用户授予 superadmin，确保可访问全局设置与跨租户管理。
	roles := pq.StringArray{RoleAdmin, RoleSuperAdmin}
	res, err := b.db.ExecContext(ctx,
		`INSERT INTO local_users (username, password_hash, display_name, is_admin, tenant_id, roles)
		 VALUES ($1,$2,$3,TRUE,1,$4) ON CONFLICT (username) DO NOTHING`,
		username, hash, username, roles)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows > 0 && generated != "" {
		log.Printf("⚠️ 首次启动：已生成随机管理员密码（用户名: %s）", username)
		log.Printf("🔑 管理员密码: %s", generated)
		log.Println("⚠️ 该密码仅显示一次，请立即登录并修改！")
	}
	return nil
}

// Name 实现 Provider。
func (b *Builtin) Name() string { return "builtin" }

// Login 校验用户名+密码，成功返回用户与会话 token。
func (b *Builtin) Login(ctx context.Context, username, password string) (*User, string, error) {
	var (
		id       int64
		hash     string
		display  string
		isAdmin  bool
		disabled bool
		tenantID int64
		roles    pq.StringArray
		updatedAt time.Time
	)
	err := b.db.QueryRowContext(ctx,
		`SELECT id, password_hash, display_name, is_admin, disabled,
		        COALESCE(tenant_id,1), COALESCE(roles, ARRAY['user']::text[]), updated_at
		 FROM local_users WHERE username=$1`,
		username).Scan(&id, &hash, &display, &isAdmin, &disabled, &tenantID, &roles, &updatedAt)
	
	// 防时序侧信道：始终做 bcrypt 比较，避免「用户不存在」比「密码错误」快。
	cmpErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err == sql.ErrNoRows || hash == "" {
		// 用户不存在：做 dummy 比较后拒绝
		_ = bcrypt.CompareHashAndPassword([]byte(b.getDummyHash()), []byte(password))
		return nil, "", ErrUnauthorized
	}
	if err != nil {
		return nil, "", err
	}
	if disabled {
		return nil, "", ErrUnauthorized
	}
	if cmpErr != nil {
		return nil, "", ErrUnauthorized
	}
	u := &User{
		ID: strconv.FormatInt(id, 10), Username: username, DisplayName: display,
		IsAdmin: isAdmin, TenantID: tenantID, Roles: []string(roles),
	}
	// 缓存用户信息（含密码已校验标记），避免后续请求重复查库
	b.cacheMu.Lock()
	b.cache[u.ID] = &cachedUser{
		Username: username, Display: display, IsAdmin: isAdmin,
		Disabled: false, TenantID: tenantID, Roles: []string(roles),
		UpdatedAt: updatedAt, CachedAt: time.Now(), PasswordOK: true,
	}
	b.cacheMu.Unlock()
	token := b.sc.sign(u)
	return u, token, nil
}

// Authenticate 从签名 cookie 解析用户并校验数据库状态（缓存 30s）。
func (b *Builtin) Authenticate(r *http.Request) (*User, error) {
	u, pl, err := b.sc.payloadFromRequest(r)
	if err != nil {
		return nil, err
	}
	// 会话已过期或被篡改，已在 sessionCodec 中拒绝；此处补充业务逻辑：
	// 查库校验用户是否被禁用、角色是否变更、最新的租户等。
	cached, err := b.lookupUser(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	if cached.Disabled {
		return nil, ErrUnauthorized
	}
	// 会话签发时间早于账号最近变更时间：可能是改密/禁用/改角色，旧会话应失效。
	if pl.issuedAt().Before(cached.UpdatedAt) {
		return nil, ErrUnauthorized
	}
	// 返回 DB 中的最新角色/租户（而非会话中的旧值），确保权限变更立即生效。
	return &User{
		ID: u.ID, Username: cached.Username, DisplayName: cached.Display,
		IsAdmin: cached.IsAdmin, TenantID: cached.TenantID, Roles: cached.Roles,
	}, nil
}

// SetSession 写入签名会话 cookie。
func (b *Builtin) SetSession(w http.ResponseWriter, token string) {
	b.sc.setCookie(w, token)
}

// ClearSession 清除会话。
func (b *Builtin) ClearSession(w http.ResponseWriter) {
	b.sc.clearCookie(w)
}

// LoginURL builtin 走本地表单，返回空。
func (b *Builtin) LoginURL(string) string { return "" }

// defaultLookup 从数据库查询用户最新状态（带缓存）。
func (b *Builtin) defaultLookup(ctx context.Context, idStr string) (*cachedUser, error) {
	// 缓存命中且未过期
	b.cacheMu.RLock()
	if cu, ok := b.cache[idStr]; ok && time.Since(cu.CachedAt) < userCacheTTL {
		b.cacheMu.RUnlock()
		return cu, nil
	}
	b.cacheMu.RUnlock()

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return nil, ErrUnauthorized
	}
	var (
		username  string
		display   string
		isAdmin   bool
		disabled  bool
		tenantID  int64
		roles     pq.StringArray
		updatedAt time.Time
	)
	err = b.db.QueryRowContext(ctx,
		`SELECT username, COALESCE(display_name,''), is_admin, disabled,
		        COALESCE(tenant_id,1), COALESCE(roles, ARRAY['user']::text[]), updated_at
		 FROM local_users WHERE id=$1`, id).
		Scan(&username, &display, &isAdmin, &disabled, &tenantID, &roles, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	cu := &cachedUser{
		Username: username, Display: display, IsAdmin: isAdmin,
		Disabled: disabled, TenantID: tenantID, Roles: []string(roles),
		UpdatedAt: updatedAt, CachedAt: time.Now(),
	}
	b.cacheMu.Lock()
	b.cache[idStr] = cu
	b.cacheMu.Unlock()
	return cu, nil
}

// evictCache 定期清理过期缓存条目（防内存泄漏）。
func (b *Builtin) evictCache() {
	ticker := time.NewTicker(userCacheTTL * 2)
	defer ticker.Stop()
	for range ticker.C {
		b.cacheMu.Lock()
		now := time.Now()
		for k, v := range b.cache {
			if now.Sub(v.CachedAt) > userCacheTTL*2 {
				delete(b.cache, k)
			}
		}
		b.cacheMu.Unlock()
	}
}

// getDummyHash 返回 dummy bcrypt 哈希（懒初始化），用于用户不存在时的恒定时间比较。
func (b *Builtin) getDummyHash() string {
	b.dummyHashOnce.Do(func() {
		h, _ := bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing-attack-mitigation"), bcrypt.DefaultCost)
		b.dummyHash = string(h)
	})
	return b.dummyHash
}

// hashOrPassthrough 明文则 bcrypt 加密；已是 $2 哈希则直接返回。
func hashOrPassthrough(pw string) (string, error) {
	if strings.HasPrefix(pw, "$2") {
		return pw, nil
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

var _ Provider = (*Builtin)(nil)
