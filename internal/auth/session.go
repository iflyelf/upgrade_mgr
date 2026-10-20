package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookie      = "upgrade_mgr_session"
	casdoorNonceCookie = "upgrade_mgr_oauth_nonce"
	sessionMaxAge      = 7 * 24 * time.Hour
)

// sessionPayload 会话载荷。
//
// 用 JSON 而非分隔符拼接：用户名/显示名可能含分隔符，
// 拼接方案会导致字段错位甚至越权（如伪造 admin 标记）。
type sessionPayload struct {
	ID       string   `json:"i"`
	Username string   `json:"u"`
	Display  string   `json:"d"`
	IsAdmin  bool     `json:"a"`
	TenantID int64    `json:"t"`
	Roles    []string `json:"r"`
	Exp      int64    `json:"e"`
	// Iat 签发时间（Unix 毫秒）；用于判定会话是否早于账号最近一次变更（改密/禁用/改角色）。
	Iat int64 `json:"at,omitempty"`
}

// issuedAt 返回签发时间；旧版会话无 Iat 时按 Exp 反推。
func (p *sessionPayload) issuedAt() time.Time {
	if p.Iat > 0 {
		return time.UnixMilli(p.Iat)
	}
	return time.Unix(p.Exp, 0).Add(-sessionMaxAge)
}

// sessionCodec 签名会话的编解码与 cookie 读写。
//
// builtin 与 casdoor 共用同一实现：会话格式只有一处定义，
// 避免两套实现随时间发散（历史上重复实现已多次引入缺陷）。
type sessionCodec struct {
	secret []byte
	secure bool
}

// newSessionCodec 创建编解码器；secret 过短则随机生成（重启后会话失效）。
func newSessionCodec(secret string, secure bool) *sessionCodec {
	b := []byte(secret)
	if len(b) < 16 {
		buf := make([]byte, 32)
		_, _ = rand.Read(buf)
		b = buf
	}
	return &sessionCodec{secret: b, secure: secure}
}

// sign 生成 base64(payload).hmac 形式的会话 token。
func (c *sessionCodec) sign(u *User) string {
	now := time.Now()
	pl := sessionPayload{
		ID: u.ID, Username: u.Username, Display: u.DisplayName,
		IsAdmin: u.IsAdmin, TenantID: u.TenantID, Roles: u.Roles,
		Exp: now.Add(sessionMaxAge).Unix(), Iat: now.UnixMilli(),
	}
	raw, _ := json.Marshal(pl)
	enc := base64.RawURLEncoding.EncodeToString(raw)
	return enc + "." + c.mac(enc)
}

// verify 校验并解析会话 token。
func (c *sessionCodec) verify(token string) (*User, error) {
	u, _, err := c.verifyPayload(token)
	return u, err
}

// verifyPayload 同 verify，额外返回原始载荷（供需要签发时间的调用方使用）。
func (c *sessionCodec) verifyPayload(token string) (*User, *sessionPayload, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, nil, ErrUnauthorized
	}
	// 先验签再解析：未通过签名的内容一律不解析，防篡改。
	if !hmac.Equal([]byte(parts[1]), []byte(c.mac(parts[0]))) {
		return nil, nil, ErrUnauthorized
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, ErrUnauthorized
	}
	var pl sessionPayload
	if err := json.Unmarshal(raw, &pl); err != nil {
		return nil, nil, ErrUnauthorized
	}
	if time.Now().Unix() > pl.Exp {
		return nil, nil, ErrUnauthorized
	}
	if pl.TenantID <= 0 {
		pl.TenantID = 1
	}
	return &User{
		ID: pl.ID, Username: pl.Username, DisplayName: pl.Display,
		IsAdmin: pl.IsAdmin, TenantID: pl.TenantID, Roles: pl.Roles,
	}, &pl, nil
}

func (c *sessionCodec) mac(s string) string {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

// fromRequest 从 cookie 解析当前用户。
func (c *sessionCodec) fromRequest(r *http.Request) (*User, error) {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, ErrUnauthorized
	}
	return c.verify(ck.Value)
}

// payloadFromRequest 从 cookie 解析用户与原始载荷。
func (c *sessionCodec) payloadFromRequest(r *http.Request) (*User, *sessionPayload, error) {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, nil, ErrUnauthorized
	}
	return c.verifyPayload(ck.Value)
}

// setCookie 写入会话 cookie。
func (c *sessionCodec) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionMaxAge.Seconds()),
	})
}

// clearCookie 清除会话 cookie。
func (c *sessionCodec) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: c.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}
