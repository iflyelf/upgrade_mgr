// Package flyiam 对接 FlyIAM 服务间接口：应用凭据、用户字段、用户与组织同步。
//
// 与 consul_mgr 保持同一协议，便于两个系统复用同一 FlyIAM 部署：
//   - GET /api/casdoor/app-credentials  自动获取 Casdoor 应用凭据（X-Service-Token）
//   - GET /api/user-fields/export       拉取「用户字段定义」，用于本系统字段列表同步
//   - GET /api/users/export             分页拉取用户（X-Total-Count）
//   - GET /api/departments/export       拉取组织树（由用户部门属性推导）
//
// 设计取舍：只做「定义/组织同步」与「凭据获取」，不做用户数据双向写回，
// 用户的增删改仍以 Casdoor 为准，避免两套系统互相覆盖。
package flyiam

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 接口路径（与 FlyIAM 约定）。
const (
	appCredentialsPath   = "/api/casdoor/app-credentials"
	userFieldsExportPath = "/api/user-fields/export"
	usersPath            = "/api/users/export"
	departmentsPath      = "/api/departments/export"
)

// AppCredentials Casdoor 应用连接凭据。
type AppCredentials struct {
	Endpoint     string `json:"endpoint"`
	Organization string `json:"organization"`
	Application  string `json:"application"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// UserFieldDef FlyIAM 导出的用户字段定义（与本地 user_field_defs 结构一致）。
type UserFieldDef struct {
	FieldKey   string `json:"fieldKey"`
	Label      string `json:"label"`
	FieldType  string `json:"fieldType"`
	Options    string `json:"options"`
	ShowInList bool   `json:"showInList"`
	ShowInForm bool   `json:"showInForm"`
	Editable   bool   `json:"editable"`
	Builtin    bool   `json:"builtin"`
	SortOrder  int    `json:"sortOrder"`
}

// Client FlyIAM 客户端。
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

// NewClient 创建客户端；endpoint 与 token 任一为空时返回 nil（表示未启用）。
func NewClient(endpoint, serviceToken string) *Client {
	endpoint = strings.TrimSpace(endpoint)
	serviceToken = strings.TrimSpace(serviceToken)
	if endpoint == "" || serviceToken == "" {
		return nil
	}
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    serviceToken,
		// 组织导出需要遍历全部用户，大组织可能超过 15s；放宽到 60s。
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// do 发起 GET 并解码通用 envelope。
func (c *Client) do(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return fmt.Errorf("构建请求失败: %w", err)
	}
	req.Header.Set("X-Service-Token", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 FlyIAM 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("FlyIAM 返回 HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("解析响应失败（疑似非 FlyIAM 接口或返回了 HTML）: %s", snippet(raw))
	}
	if env.Code != 0 {
		return fmt.Errorf("FlyIAM 返回错误: %s", env.Message)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("解析 data 失败: %w", err)
	}
	return nil
}

// FetchAppCredentials 获取 Casdoor 应用凭据。
func (c *Client) FetchAppCredentials(ctx context.Context) (*AppCredentials, error) {
	if c == nil {
		return nil, fmt.Errorf("未配置 FlyIAM 地址或服务令牌")
	}
	var creds AppCredentials
	if err := c.do(ctx, appCredentialsPath, &creds); err != nil {
		return nil, err
	}
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return nil, fmt.Errorf("FlyIAM 未返回有效应用凭据")
	}
	return &creds, nil
}

// FetchUserFields 拉取用户字段定义。
func (c *Client) FetchUserFields(ctx context.Context) ([]UserFieldDef, error) {
	if c == nil {
		return nil, fmt.Errorf("未配置 FlyIAM 地址或服务令牌")
	}
	var defs []UserFieldDef
	if err := c.do(ctx, userFieldsExportPath, &defs); err != nil {
		return nil, err
	}
	return defs, nil
}

// RemoteUser FlyIAM 导出的用户（只读展示用）。
//
// 只取展示所需字段；本系统不写回用户数据（用户以 Casdoor 为准）。
type RemoteUser struct {
	Username    string            `json:"username"`
	DisplayName string            `json:"displayName"`
	Email       string            `json:"email"`
	Phone       string            `json:"phone"`
	Properties  map[string]string `json:"properties"`
}

// Department FlyIAM 导出的组织节点。
type Department struct {
	DeptID    string `json:"deptId"`
	DeptName  string `json:"deptName"`
	ParentID  string `json:"parentId"`
	Level     int    `json:"level"`
	FullPath  string `json:"fullPath"`
	UserCount int    `json:"userCount"`
}

// FetchUsers 分页拉取全部用户（只读；用于人员页对照、一键开通与组织同步）。
//
// 服务端按 X-Total-Count 返回总数，这里以「本页不足 pageSize」或
// 「本页没有新增用户」作为结束条件，兼容不支持分页的旧实现。
func (c *Client) FetchUsers(ctx context.Context) ([]RemoteUser, error) {
	if c == nil {
		return nil, fmt.Errorf("未配置 FlyIAM 地址或服务令牌")
	}
	const pageSize = 500
	seen := map[string]bool{}
	all := make([]RemoteUser, 0, pageSize)
	for page := 1; page <= 400; page++ {
		var batch []RemoteUser
		path := fmt.Sprintf("%s?page=%d&pageSize=%d", usersPath, page, pageSize)
		if err := c.do(ctx, path, &batch); err != nil {
			return nil, err
		}
		added := 0
		for _, u := range batch {
			u.Username = strings.TrimSpace(u.Username)
			if u.Username == "" || seen[u.Username] {
				continue
			}
			seen[u.Username] = true
			all = append(all, u)
			added++
		}
		if len(batch) < pageSize || added == 0 {
			break
		}
	}
	return all, nil
}

// FetchDepartments 拉取组织树（节点按 fullPath 排序，人数为最深一级部门的用户数）。
func (c *Client) FetchDepartments(ctx context.Context) ([]Department, error) {
	if c == nil {
		return nil, fmt.Errorf("未配置 FlyIAM 地址或服务令牌")
	}
	var deps []Department
	if err := c.do(ctx, departmentsPath, &deps); err != nil {
		return nil, err
	}
	return deps, nil
}

// HealthCheck 探测 FlyIAM 可用性（用于设置页「测试连接」）。
func (c *Client) HealthCheck(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("未配置 FlyIAM 地址或服务令牌")
	}
	var creds AppCredentials
	return c.do(ctx, appCredentialsPath, &creds)
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
