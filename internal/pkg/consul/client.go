// Package consul 是 upgrade_mgr 自有的 Consul 只读客户端封装（可选依赖）。
//
// 独立实现，不引用 consul_mgr。关键点：
//   - 禁用 HTTP 代理（Consul 在内网，HTTP_PROXY 会导致超时）；
//   - 放宽连接池以支撑批量 Catalog 查询；
//   - 不调用 Agent().Self()（只读网关/LB 后不可达，会拖满超时），
//     数据中心以配置为准。
package consul

import (
	"fmt"
	"net/http"
	"time"

	capi "github.com/hashicorp/consul/api"
)

// Config Consul 连接配置。
type Config struct {
	Address    string
	Token      string
	Datacenter string
	Timeout    time.Duration
}

// Client 只读 Consul 客户端。
type Client struct {
	api *capi.Client
	cfg Config
}

// New 创建客户端。
func New(cfg Config) (*Client, error) {
	if cfg.Address == "" {
		return nil, fmt.Errorf("consul 地址为空")
	}
	conf := capi.DefaultConfig()
	conf.Address = cfg.Address
	conf.Token = cfg.Token
	conf.Datacenter = cfg.Datacenter

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	conf.HttpClient = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               nil, // 内网，禁用代理
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 64,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	c, err := capi.NewClient(conf)
	if err != nil {
		return nil, fmt.Errorf("创建 Consul 客户端失败: %w", err)
	}
	return &Client{api: c, cfg: cfg}, nil
}

// TestConnection 通过 Catalog 数据中心探测连通性（不走 Agent API）。
func (c *Client) TestConnection() error {
	_, err := c.api.Catalog().Datacenters()
	if err != nil {
		return fmt.Errorf("Consul 连接失败: %w", err)
	}
	return nil
}

// ServiceInstance 一个服务实例的精简视图。
type ServiceInstance struct {
	ServiceName string
	Address     string
	Port        int
	Node        string
	Datacenter  string
	Tags        []string
	Meta        map[string]string
}

// Services 返回所有服务名 → tags。
func (c *Client) Services() (map[string][]string, error) {
	svcs, _, err := c.api.Catalog().Services(nil)
	if err != nil {
		return nil, fmt.Errorf("获取服务列表失败: %w", err)
	}
	return svcs, nil
}

// ServiceInstances 返回某服务的全部实例。
func (c *Client) ServiceInstances(name string) ([]ServiceInstance, error) {
	entries, _, err := c.api.Catalog().Service(name, "", nil)
	if err != nil {
		return nil, fmt.Errorf("获取服务 %s 实例失败: %w", name, err)
	}
	out := make([]ServiceInstance, 0, len(entries))
	for _, e := range entries {
		addr := e.ServiceAddress
		if addr == "" {
			addr = e.Address
		}
		out = append(out, ServiceInstance{
			ServiceName: e.ServiceName,
			Address:     addr,
			Port:        e.ServicePort,
			Node:        e.Node,
			Datacenter:  e.Datacenter,
			Tags:        e.ServiceTags,
			Meta:        e.ServiceMeta,
		})
	}
	return out, nil
}

// API 暴露原生客户端（供后续 Meta 回写等高级操作使用）。
func (c *Client) API() *capi.Client { return c.api }

// WriteServiceMeta 升级成功后回写服务实例 Meta（供 consul_mgr 展示最新版本）。
//
// P4 实现：遍历 Catalog 找到 ServiceAddress==ip 的实例，重新注册以更新 Meta。
// 需要 Consul 写权限；失败不阻塞升级流程（best-effort）。
func (c *Client) WriteServiceMeta(ip, key, value string) error {
	if c == nil || c.api == nil {
		return nil
	}
	services, _, err := c.api.Catalog().Services(nil)
	if err != nil {
		return err
	}
	for name := range services {
		entries, _, err := c.api.Catalog().Service(name, "", nil)
		if err != nil {
			continue
		}
		for _, e := range entries {
			addr := e.ServiceAddress
			if addr == "" {
				addr = e.Address
			}
			if addr != ip {
				continue
			}
			meta := e.ServiceMeta
			if meta == nil {
				meta = map[string]string{}
			}
			meta[key] = value
			reg := &capi.CatalogRegistration{
				Node:    e.Node,
				Address: e.Address,
				Service: &capi.AgentService{
					ID: e.ServiceID, Service: e.ServiceName,
					Address: e.ServiceAddress, Port: e.ServicePort,
					Tags: e.ServiceTags, Meta: meta,
				},
			}
			if _, err := c.api.Catalog().Register(reg, nil); err != nil {
				return err
			}
		}
	}
	return nil
}
