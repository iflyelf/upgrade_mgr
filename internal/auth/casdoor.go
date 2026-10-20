package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Casdoor FlyIAM/Casdoor SSO 认证提供方（OAuth2 授权码流程）。
//
// 流程：
//  1. 前端访问 LoginURL() 跳转到 Casdoor 授权页（携带带签名的 state 防 CSRF）；
//  2. 用户在 Casdoor 完成认证后回调 redirectURI?code=...&state=...；
//  3. HandleCallback 校验 state → 用 code 换 access_token → 拉取用户信息；
//  4. 将 Casdoor 身份映射为本地 User，签发本系统自己的会话 cookie。
//
// 说明：会话仍由本系统签发（与 builtin 共用 sessionCodec），
// 这样中间件、租户作用域等逻辑对两种认证方式完全一致。
type Casdoor struct {
	endpoint       string // 服务端到 Casdoor 的地址（换 token / 取用户信息）
	publicEndpoint string // 浏览器可达地址（授权跳转）
	clientID       string
	clientSecret   string
	organization   string
	application    string
	redirectURI    string
	session        *sessionCodec
	http           *http.Client
	// stateKey 用于签名 state，防 CSRF 与 state 伪造。
	stateKey []byte
	// cookieSecure 是否仅 HTTPS 传输 cookie（影响 nonce cookie）。
	cookieSecure bool
}

// CasdoorOptions 构造参数。
type CasdoorOptions struct {
	Endpoint       string
	PublicEndpoint string
	ClientID       string
	ClientSecret   string
	Organization   string
	Application    string
	// RedirectURI 回调地址，需与 Casdoor 应用配置一致，如
	// https://upgrade.example.com/api/auth/callback
	RedirectURI string
	// SessionSecret 本系统会话签名密钥（与 builtin 同源）。
	SessionSecret string
	// CookieSecure 是否仅经 HTTPS 传输会话 cookie。
	CookieSecure bool
}

// NewCasdoor 创建 Casdoor 认证提供方。
func NewCasdoor(o CasdoorOptions) *Casdoor {
	key := []byte(o.SessionSecret)
	if len(key) < 16 {
		buf := make([]byte, 32)
		_, _ = rand.Read(buf)
		key = buf
	}
	return &Casdoor{
		endpoint:       strings.TrimRight(o.Endpoint, "/"),
		publicEndpoint: strings.TrimRight(o.PublicEndpoint, "/"),
		clientID:       o.ClientID,
		clientSecret:   o.ClientSecret,
		organization:   o.Organization,
		application:    o.Application,
		redirectURI:    o.RedirectURI,
		session:        newSessionCodec(o.SessionSecret, o.CookieSecure),
		http:           &http.Client{Timeout: 15 * time.Second},
		stateKey:       key,
		cookieSecure:   o.CookieSecure,
	}
}

func (c *Casdoor) Name() string { return "casdoor" }

// Login Casdoor 不使用本地密码登录：走 OAuth 跳转。
func (c *Casdoor) Login(context.Context, string, string) (*User, string, error) {
	return nil, "", fmt.Errorf("casdoor 模式请使用 SSO 登录（GET /api/auth/login-url）")
}

// Authenticate 校验本系统签发的会话 cookie。
func (c *Casdoor) Authenticate(r *http.Request) (*User, error) {
	return c.session.fromRequest(r)
}

// SetSession 写入会话 cookie。
func (c *Casdoor) SetSession(w http.ResponseWriter, token string) {
	c.session.setCookie(w, token)
}

// ClearSession 清除会话 cookie。
func (c *Casdoor) ClearSession(w http.ResponseWriter) {
	c.session.clearCookie(w)
}

// LoginURL 返回 Casdoor 授权页地址。
//
// 已废弃：请使用 BeginLogin，它会将 nonce 绑定到 cookie 以防 CSRF。
// 为保持向后兼容，nonce 参数仍可传入，但不推荐直接使用此方法。
func (c *Casdoor) LoginURL(nonce string) string {
	base := c.publicEndpoint
	if base == "" {
		base = c.endpoint
	}
	if base == "" || c.clientID == "" {
		return ""
	}
	q := url.Values{}
	q.Set("client_id", c.clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", c.redirectURI)
	q.Set("scope", "read")
	q.Set("state", c.signState(nonce))
	return base + "/login/oauth/authorize?" + q.Encode()
}

// BeginLogin 启动 OAuth 流程：生成 nonce、写入 HttpOnly cookie、返回授权页 URL。
//
// 前端调用此方法获取 login_url 后跳转，回调时通过 HandleCallbackRequest 校验 nonce。
func (c *Casdoor) BeginLogin(w http.ResponseWriter) string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	nonce := hex.EncodeToString(buf)
	// nonce cookie：HttpOnly、SameSite=Lax（允许顶级导航携带），10 分钟有效期。
	http.SetCookie(w, &http.Cookie{
		Name:     casdoorNonceCookie,
		Value:    nonce,
		Path:     "/api/auth/callback",
		HttpOnly: true,
		Secure:   c.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	return c.LoginURL(nonce)
}

// HandleCallbackRequest 从请求中读取 nonce cookie，校验 state，完成授权流程。
//
// 成功后清除 nonce cookie（用后即焚），返回用户与会话 token。
func (c *Casdoor) HandleCallbackRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) (*User, string, error) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	nonceCk, err := r.Cookie(casdoorNonceCookie)
	if err != nil {
		return nil, "", fmt.Errorf("缺少 nonce cookie（可能的 CSRF）")
	}
	expectedNonce := nonceCk.Value
	// 清除 nonce cookie（用后即焚）
	http.SetCookie(w, &http.Cookie{
		Name: casdoorNonceCookie, Value: "", Path: "/api/auth/callback",
		HttpOnly: true, Secure: c.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	if !c.verifyStateWithNonce(state, expectedNonce) {
		return nil, "", fmt.Errorf("state 与 nonce 不匹配（可能的 CSRF）")
	}
	return c.HandleCallback(ctx, code, state)
}

// HandleCallback 处理 OAuth 回调：校验 state → 换 token → 取用户信息 → 签发本地会话。
//
// 返回用户与本系统会话 token（由调用方写入 cookie）。
func (c *Casdoor) HandleCallback(ctx context.Context, code, state string) (*User, string, error) {
	if c.clientID == "" || c.clientSecret == "" || c.endpoint == "" {
		return nil, "", fmt.Errorf("casdoor 未正确配置")
	}
	if code == "" {
		return nil, "", fmt.Errorf("缺少授权码")
	}
	if !c.verifyState(state) {
		// state 校验失败通常意味着 CSRF 或链接被伪造，必须拒绝。
		return nil, "", fmt.Errorf("state 校验失败（可能的 CSRF）")
	}

	accessToken, err := c.exchangeToken(ctx, code)
	if err != nil {
		return nil, "", err
	}
	u, err := c.fetchUser(ctx, accessToken)
	if err != nil {
		return nil, "", err
	}
	return u, c.session.sign(u), nil
}

// exchangeToken 用授权码换取 access_token。
func (c *Casdoor) exchangeToken(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", c.redirectURI)

	req, err := http.NewRequestWithContext(ctx, "POST",
		c.endpoint+"/api/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 Casdoor 换取 token 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Casdoor 返回 %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("解析 token 响应失败: %w", err)
	}
	if tr.Error != "" {
		return "", fmt.Errorf("Casdoor 拒绝授权: %s", tr.Error)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("Casdoor 未返回 access_token")
	}
	return tr.AccessToken, nil
}

// casdoorUser Casdoor 用户信息（仅取所需字段）。
type casdoorUser struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Email       string   `json:"email"`
	IsAdmin     bool     `json:"isAdmin"`
	IsForbidden bool     `json:"isForbidden"`
	Owner       string   `json:"owner"`
	Roles       []string `json:"-"`
}

// fetchUser 拉取当前登录用户信息并映射为本地 User。
func (c *Casdoor) fetchUser(ctx context.Context, accessToken string) (*User, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.endpoint+"/api/get-account", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取 Casdoor 用户信息失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Casdoor 用户接口返回 %d", resp.StatusCode)
	}

	// Casdoor 常见响应为 {status, data:{...}}，也兼容直接返回用户对象。
	var wrapper struct {
		Status string          `json:"status"`
		Msg    string          `json:"msg"`
		Data   json.RawMessage `json:"data"`
	}
	raw := body
	if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Data) > 0 {
		if wrapper.Status != "" && wrapper.Status != "ok" {
			return nil, fmt.Errorf("Casdoor: %s", wrapper.Msg)
		}
		raw = wrapper.Data
	}

	var cu casdoorUser
	if err := json.Unmarshal(raw, &cu); err != nil {
		return nil, fmt.Errorf("解析用户信息失败: %w", err)
	}
	if cu.Name == "" {
		return nil, fmt.Errorf("Casdoor 未返回用户名")
	}
	if cu.IsForbidden {
		return nil, ErrUnauthorized
	}
	// 组织校验：配置了组织时，仅接受该组织用户，避免跨组织越权登录。
	if c.organization != "" && cu.Owner != "" && cu.Owner != c.organization {
		return nil, fmt.Errorf("用户不属于组织 %s", c.organization)
	}

	display := cu.DisplayName
	if display == "" {
		display = cu.Name
	}
	roles := []string{RoleUser}
	if cu.IsAdmin {
		roles = []string{RoleAdmin}
	}
	return &User{
		ID:          cu.Name,
		Username:    cu.Name,
		DisplayName: display,
		IsAdmin:     cu.IsAdmin,
		// SSO 用户默认归属默认租户；如需按组织映射租户可在此扩展。
		TenantID: 1,
		Roles:    roles,
	}, nil
}

// --- state 签名（防 CSRF / 伪造回调） ---

// signState 生成带签名与时效的 state。
func (c *Casdoor) signState(nonce string) string {
	if nonce == "" {
		buf := make([]byte, 8)
		_, _ = rand.Read(buf)
		nonce = hex.EncodeToString(buf)
	}
	payload := fmt.Sprintf("%s.%d", nonce, time.Now().Unix())
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return enc + "." + c.macState(enc)
}

// verifyState 校验 state 签名与时效（10 分钟）。
func (c *Casdoor) verifyState(state string) bool {
	_, ok := c.verifyStateAndExtractNonce(state)
	return ok
}

// verifyStateWithNonce 校验 state 并比对嵌入的 nonce（常量时间比较）。
func (c *Casdoor) verifyStateWithNonce(state, expectedNonce string) bool {
	nonce, ok := c.verifyStateAndExtractNonce(state)
	if !ok {
		return false
	}
	return hmac.Equal([]byte(nonce), []byte(expectedNonce))
}

// verifyStateAndExtractNonce 校验 state 签名与时效，返回嵌入的 nonce。
func (c *Casdoor) verifyStateAndExtractNonce(state string) (string, bool) {
	parts := strings.Split(state, ".")
	if len(parts) != 2 {
		return "", false
	}
	if !hmac.Equal([]byte(parts[1]), []byte(c.macState(parts[0]))) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	seg := strings.Split(string(raw), ".")
	if len(seg) != 2 {
		return "", false
	}
	var ts int64
	if _, err := fmt.Sscanf(seg[1], "%d", &ts); err != nil {
		return "", false
	}
	age := time.Since(time.Unix(ts, 0))
	if age < 0 || age > 10*time.Minute {
		return "", false
	}
	return seg[0], true
}

func (c *Casdoor) macState(s string) string {
	m := hmac.New(sha256.New, c.stateKey)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

var _ Provider = (*Casdoor)(nil)
