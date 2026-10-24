// Package consulmgr 是 consul_mgr 平台的只读客户端（可选依赖）。
//
// 为什么不直接连 Consul：
//
//	consul_mgr 的每个「服务组」各自配置独立的 Consul 地址与令牌，
//	一个环境常有多套 Consul。upgrade_mgr 的 consul 配置只能连一套，
//	通过 consul_mgr 则能一次拿到全部服务组的主机，并带上服务组归属。
//
// 协议：GET /api/service/hosts，请求头 X-Service-Token 携带服务令牌
// （与 FlyIAM 的服务间调用协议一致）。该接口只读，不返回 consul_token 等凭据。
package consulmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ServiceTokenHeader 服务间调用令牌请求头。
const ServiceTokenHeader = "X-Service-Token"

// Host consul_mgr 导出的一台主机（按 IP 聚合）。
//
// Meta 为该 IP 上所有服务实例 ServiceMeta 的并集，由调用方按映射规则取用。
type Host struct {
	IP         string            `json:"ip"`
	Node       string            `json:"node"`
	Datacenter string            `json:"datacenter"`
	GroupID    int64             `json:"group_id"`
	GroupName  string            `json:"group_name"`
	GroupCode  string            `json:"group_code"`
	Services   []string          `json:"services"`
	Tags       []string          `json:"tags"`
	Meta       map[string]string `json:"meta"`
	Health     string            `json:"health"`
}

// ExportResult 主机清单导出结果。
type ExportResult struct {
	Hosts        []Host   `json:"hosts"`
	Groups       int      `json:"groups"`
	FailedGroups []string `json:"failed_groups"`
	// Partial 为 true 表示部分服务组查询失败，清单不完整；
	// 调用方必须据此跳过「清理已移除主机」等破坏性操作。
	Partial bool `json:"partial"`
}

// Client consul_mgr 只读客户端。
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

// NewClient 创建客户端；endpoint 或 token 为空返回 nil（视为未配置）。
func NewClient(endpoint, token string) *Client {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	token = strings.TrimSpace(token)
	if endpoint == "" || token == "" {
		return nil
	}
	return &Client{
		endpoint: endpoint,
		token:    token,
		// 全量主机可能上万条，给足读取时间；超时由调用方 ctx 进一步收紧。
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// NewClientTimeout 同 NewClient，但显式指定 HTTP 超时（秒）。
//
// 主机规模大时单次导出可能很慢，超时在「设置 → consul_mgr 对接」中可调；
// timeout<=0 时回退到默认值。
func NewClientTimeout(endpoint, token string, timeout time.Duration) *Client {
	c := NewClient(endpoint, token)
	if c != nil && timeout > 0 {
		c.http = &http.Client{Timeout: timeout}
	}
	return c
}

// Endpoint 返回配置的地址（用于日志与页面展示，不含令牌）。
func (c *Client) Endpoint() string {
	if c == nil {
		return ""
	}
	return c.endpoint
}

// respEnvelope consul_mgr 的统一响应包装 {code,message,data}。
type respEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// FetchHosts 拉取主机清单；groupID>0 时只取指定服务组。
func (c *Client) FetchHosts(ctx context.Context, groupID int64) (*ExportResult, error) {
	if c == nil {
		return nil, fmt.Errorf("consul_mgr 未配置")
	}
	u := c.endpoint + "/api/service/hosts"
	if groupID > 0 {
		u += "?" + url.Values{"group_id": {strconv.FormatInt(groupID, 10)}}.Encode()
	}
	var out ExportResult
	if err := c.getJSON(ctx, u, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Group consul_mgr 中一个可同步的服务组。
//
// 不含 consul_address / consul_token：那是 consul_mgr 侧的凭据，
// 导出接口不返回，本结构也不保留。
type Group struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Code       string `json:"code"`
	Datacenter string `json:"datacenter"`
}

// FetchGroups 拉取可同步的服务组列表（供页面下拉选择）。
//
// 只返回 consul_mgr 侧「启用且已配置 Consul 地址」的组，
// 与 FetchHosts 的筛选口径一致：能选到的组同步时一定会被处理。
func (c *Client) FetchGroups(ctx context.Context) ([]Group, error) {
	if c == nil {
		return nil, fmt.Errorf("consul_mgr 未配置")
	}
	var out struct {
		Groups []Group `json:"groups"`
	}
	if err := c.getJSON(ctx, c.endpoint+"/api/service/groups", &out); err != nil {
		return nil, err
	}
	return out.Groups, nil
}

// Ping 校验地址可达与令牌有效。
//
// consul_mgr 没有专门的服务间健康检查接口，这里复用服务组列表：
// 它只查一次数据库、不触碰 Consul，比拉取全量主机轻得多。
// 令牌错误返回 401，未配置令牌返回 503，均能明确区分。
func (c *Client) Ping(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("consul_mgr 未配置")
	}
	_, err := c.FetchGroups(ctx)
	return err
}

// getJSON 发起 GET 并解析 consul_mgr 的响应包装。
func (c *Client) getJSON(ctx context.Context, rawURL string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set(ServiceTokenHeader, c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 consul_mgr 失败: %w", err)
	}
	defer resp.Body.Close()

	// consul_mgr 对业务错误用 code 字段（HTTP 仍可能是 200），
	// 对鉴权错误用 401/503。两种都要识别。
	var env respEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("consul_mgr 返回 HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("解析 consul_mgr 响应失败: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("服务令牌无效，请核对两端的令牌是否一致")
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("consul_mgr 未开放服务间 API：%s", env.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("consul_mgr 返回 HTTP %d: %s", resp.StatusCode, env.Message)
	}
	// code 200 为成功（consul_mgr 的 response.Success 固定写 200）。
	if env.Code != 200 && env.Code != 0 {
		return fmt.Errorf("consul_mgr 返回错误: %s", env.Message)
	}
	if len(env.Data) == 0 {
		return fmt.Errorf("consul_mgr 返回空数据")
	}
	if err := json.Unmarshal(env.Data, dest); err != nil {
		return fmt.Errorf("解析 consul_mgr 数据失败: %w", err)
	}
	return nil
}
