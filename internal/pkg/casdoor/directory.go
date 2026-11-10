// Package casdoor 只读访问 Casdoor 用户目录。
//
// 认证方式：Casdoor 的 /api/get-users 支持用应用凭据做 HTTP Basic 认证
// （client_id:client_secret），因此本系统无需保存 Casdoor 管理员密码，
// 也不需要先登录 FlyIAM。对比 FlyIAM 的 /api/users/export（3 万用户需要
// 60+ 次串行请求，必然超时），Casdoor 单次请求即可返回「当前页 + 总数」。
package casdoor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DirectoryUser Casdoor 用户目录条目（仅保留展示所需字段）。
type DirectoryUser struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName"`
	Email       string            `json:"email"`
	Phone       string            `json:"phone"`
	Avatar      string            `json:"avatar"`
	Owner       string            `json:"owner"`
	IsAdmin     bool              `json:"isAdmin"`
	IsForbidden bool              `json:"isForbidden"`
	Properties  map[string]string `json:"properties"`
	CreatedTime string            `json:"createdTime"`
}

// Directory Casdoor 用户目录客户端。
type Directory struct {
	endpoint     string
	clientID     string
	clientSecret string
	organization string
	application  string
	http         *http.Client
}

// NewDirectory 创建目录客户端。
//
// endpoint 为服务端可达的 Casdoor 地址（如 http://flysso.iflytek.com）。
// application 可选：创建用户时写入 signupApplication，留空则沿用 Casdoor 默认值。
func NewDirectory(endpoint, clientID, clientSecret, organization string, application ...string) *Directory {
	app := ""
	if len(application) > 0 {
		app = strings.TrimSpace(application[0])
	}
	return &Directory{
		endpoint:     strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		organization: strings.TrimSpace(organization),
		application:  app,
		http:         &http.Client{Timeout: 20 * time.Second},
	}
}

// Valid 配置是否完整。
func (d *Directory) Valid() bool {
	return d != nil && d.endpoint != "" && d.clientID != "" && d.clientSecret != ""
}

// List 服务端分页查询用户。
//
// field 取值：name / displayName / email / phone；keyword 为空时返回全部。
// 返回当前页列表与总数（Casdoor 的 data2）。
func (d *Directory) List(ctx context.Context, page, size int, field, keyword string) ([]DirectoryUser, int, error) {
	if !d.Valid() {
		return nil, 0, fmt.Errorf("Casdoor 未配置（endpoint/client_id/client_secret）")
	}
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	switch field {
	case "name", "displayName", "email", "phone":
	default:
		field = "name"
	}

	q := url.Values{}
	q.Set("owner", d.organization)
	q.Set("p", strconv.Itoa(page))
	q.Set("pageSize", strconv.Itoa(size))
	// 按唯一键 name 升序，保证 offset 分页结果稳定（否则翻页可能重复/漏读）。
	q.Set("sortField", "name")
	q.Set("sortOrder", "ascend")
	if strings.TrimSpace(keyword) != "" {
		q.Set("field", field)
		q.Set("value", strings.TrimSpace(keyword))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		d.endpoint+"/api/get-users?"+q.Encode(), nil)
	if err != nil {
		return nil, 0, err
	}
	// Casdoor 对应用凭据开放 Basic 认证，等价于 SDK 的 GetPaginationUsers。
	req.SetBasicAuth(d.clientID, d.clientSecret)

	resp, err := d.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求 Casdoor 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("Casdoor 返回 HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	var env struct {
		Status string          `json:"status"`
		Msg    string          `json:"msg"`
		Data   []DirectoryUser `json:"data"`
		Data2  json.Number     `json:"data2"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, 0, fmt.Errorf("解析 Casdoor 响应失败（疑似非 Casdoor 地址）: %s", snippet(body))
	}
	if env.Status != "" && env.Status != "ok" {
		return nil, 0, fmt.Errorf("Casdoor: %s", env.Msg)
	}
	total := len(env.Data)
	if n, err := env.Data2.Int64(); err == nil && n >= 0 {
		total = int(n)
	}
	return env.Data, total, nil
}

// Get 查询单个用户（不存在返回 nil, nil）。
func (d *Directory) Get(ctx context.Context, name string) (*DirectoryUser, error) {
	if !d.Valid() {
		return nil, fmt.Errorf("Casdoor 未配置（endpoint/client_id/client_secret）")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("id", d.organization+"/"+name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		d.endpoint+"/api/get-user?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(d.clientID, d.clientSecret)
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Casdoor 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Casdoor 返回 HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var env struct {
		Status string         `json:"status"`
		Msg    string         `json:"msg"`
		Data   *DirectoryUser `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("解析 Casdoor 响应失败: %s", snippet(body))
	}
	if env.Status != "" && env.Status != "ok" {
		return nil, fmt.Errorf("Casdoor: %s", env.Msg)
	}
	return env.Data, nil
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
