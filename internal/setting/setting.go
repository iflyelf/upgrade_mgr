// Package setting 应用设置：页面可配置，DB 优先 / env 兜底。
//
// 设计同 consul_mgr：设置项以 key/value 存 app_settings 表；启动时 Load 应用到
// 共享的 *config.Config（指针），使既有读取点自动生效；页面修改后写 DB 并即时写回。
package setting

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/iflyelf/upgrade_mgr/internal/config"
)

// Item 可配置项定义。
type Item struct {
	Key      string
	Group    string
	Label    string
	Type     string // string / int / bool / list / secret
	Secret   bool
	Get      func(*config.Config) string
	Set      func(*config.Config, string)
	Validate func(string) error // 可选：保存前校验，失败则整批设置不落库
}

// Registry 全部可页面配置项。
var Registry = []Item{
	// 服务器
	{Key: "server.external_url", Group: "服务器", Label: "对外访问地址 external_url（生成 Agent 安装命令，如 https://upgrade.example.com）", Type: "string",
		Get:      func(c *config.Config) string { return c.Server.ExternalURL },
		Set:      func(c *config.Config, v string) { c.Server.ExternalURL = normalizeExternalURL(v) },
		Validate: validateExternalURL},

	// Consul（可选数据源）
	{Key: "consul.enabled", Group: "Consul", Label: "启用 Consul 同步", Type: "bool",
		Get: func(c *config.Config) string { return b2s(c.Consul.Enabled) },
		Set: func(c *config.Config, v string) { c.Consul.Enabled = v == "true" }},
	{Key: "consul.address", Group: "Consul", Label: "地址", Type: "string",
		Get: func(c *config.Config) string { return c.Consul.Address },
		Set: func(c *config.Config, v string) { c.Consul.Address = v }},
	{Key: "consul.token", Group: "Consul", Label: "Token", Type: "secret", Secret: true,
		Get: func(c *config.Config) string { return c.Consul.Token },
		Set: func(c *config.Config, v string) { c.Consul.Token = v }},
	{Key: "consul.datacenter", Group: "Consul", Label: "数据中心（留空自动探测）", Type: "string",
		Get: func(c *config.Config) string { return c.Consul.Datacenter },
		Set: func(c *config.Config, v string) { c.Consul.Datacenter = v }},
	{Key: "consul.sync_interval", Group: "Consul", Label: "同步间隔(5m/1h)", Type: "string",
		Get: func(c *config.Config) string { return c.Consul.SyncInterval },
		Set: func(c *config.Config, v string) { c.Consul.SyncInterval = v }},
	{Key: "consul.cleanup_missing", Group: "Consul", Label: "自动清理 Consul 中已移除的主机（仅纯 Consul 主机，有 Agent/任务时保留）", Type: "bool",
		Get: func(c *config.Config) string { return b2s(c.Consul.CleanupMissing) },
		Set: func(c *config.Config, v string) { c.Consul.CleanupMissing = v == "true" }},

	// 认证
	{Key: "auth.provider", Group: "认证", Label: "认证方式(builtin/casdoor)", Type: "string",
		Get: func(c *config.Config) string { return c.Auth.Provider },
		Set: func(c *config.Config, v string) { c.Auth.Provider = v }},

	// Casdoor（可选 SSO）
	{Key: "casdoor.endpoint", Group: "Casdoor", Label: "后端地址", Type: "string",
		Get: func(c *config.Config) string { return c.Casdoor.Endpoint },
		Set: func(c *config.Config, v string) { c.Casdoor.Endpoint = v }},
	{Key: "casdoor.public_endpoint", Group: "Casdoor", Label: "浏览器地址", Type: "string",
		Get: func(c *config.Config) string { return c.Casdoor.PublicEndpoint },
		Set: func(c *config.Config, v string) { c.Casdoor.PublicEndpoint = v }},
	{Key: "casdoor.organization", Group: "Casdoor", Label: "组织名", Type: "string",
		Get: func(c *config.Config) string { return c.Casdoor.OrganizationName },
		Set: func(c *config.Config, v string) { c.Casdoor.OrganizationName = v }},
	{Key: "casdoor.application", Group: "Casdoor", Label: "应用名", Type: "string",
		Get: func(c *config.Config) string { return c.Casdoor.ApplicationName },
		Set: func(c *config.Config, v string) { c.Casdoor.ApplicationName = v }},
	{Key: "casdoor.client_id", Group: "Casdoor", Label: "Client ID", Type: "string",
		Get: func(c *config.Config) string { return c.Casdoor.ClientId },
		Set: func(c *config.Config, v string) { c.Casdoor.ClientId = v }},
	{Key: "casdoor.client_secret", Group: "Casdoor", Label: "Client Secret", Type: "secret", Secret: true,
		Get: func(c *config.Config) string { return c.Casdoor.ClientSecret },
		Set: func(c *config.Config, v string) { c.Casdoor.ClientSecret = v }},
	{Key: "casdoor.certificate", Group: "Casdoor", Label: "证书", Type: "secret", Secret: true,
		Get: func(c *config.Config) string { return c.Casdoor.Certificate },
		Set: func(c *config.Config, v string) { c.Casdoor.Certificate = v }},

	// 制品
	{Key: "artifact.dir", Group: "制品", Label: "存储目录", Type: "string",
		Get: func(c *config.Config) string { return c.Artifact.Dir },
		Set: func(c *config.Config, v string) { c.Artifact.Dir = v }},

	// Agent / 指令
	{Key: "agent.poll_hold", Group: "Agent", Label: "长轮询挂起(秒)", Type: "int",
		Get: func(c *config.Config) string { return strconv.Itoa(c.Agent.PollHold) },
		Set: func(c *config.Config, v string) { setInt(&c.Agent.PollHold, v) }},
	{Key: "agent.lease_max", Group: "Agent", Label: "维护窗口上限(1h)", Type: "string",
		Get: func(c *config.Config) string { return c.Agent.LeaseMax },
		Set: func(c *config.Config, v string) { c.Agent.LeaseMax = v }},
	{Key: "agent.allow_exec_mode", Group: "Agent", Label: "允许 exec/script 模式与命令任务", Type: "bool",
		Get: func(c *config.Config) string { return b2s(c.Agent.AllowExecMode) },
		Set: func(c *config.Config, v string) { c.Agent.AllowExecMode = v == "true" }},
	{Key: "agent.auto_update", Group: "Agent", Label: "自动升级旧 Agent（下载新二进制并重启 Agent）", Type: "bool",
		Get: func(c *config.Config) string { return b2s(c.Agent.AutoUpdate) },
		Set: func(c *config.Config, v string) { c.Agent.AutoUpdate = v == "true" }},

	// 日志
	{Key: "log.level", Group: "日志", Label: "级别", Type: "string",
		Get: func(c *config.Config) string { return c.Log.Level },
		Set: func(c *config.Config, v string) { c.Log.Level = v }},
}

var registryMap = func() map[string]Item {
	m := make(map[string]Item, len(Registry))
	for _, it := range Registry {
		m[it.Key] = it
	}
	return m
}()

// Service 设置服务。
type Service struct {
	mu    sync.RWMutex
	db    *sql.DB
	cfg   *config.Config
	items map[string]Item
}

// NewService 创建设置服务。
func NewService(db *sql.DB, cfg *config.Config) *Service {
	return &Service{db: db, cfg: cfg, items: registryMap}
}

// Load 从数据库加载设置并应用到 Config。
func (s *Service) Load(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM app_settings`)
	if err != nil {
		return fmt.Errorf("读取应用设置失败: %w", err)
	}
	defer rows.Close()

	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		if it, ok := s.items[k]; ok {
			it.Set(s.cfg, v)
		}
	}
	return rows.Err()
}

// Apply 写入 DB 并即时应用到内存 Config。返回实际生效的 key 列表。
func (s *Service) Apply(ctx context.Context, kv map[string]string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 先整体校验，再落库：避免某一项非法时，其他项已经部分生效。
	for k, v := range kv {
		it, ok := s.items[k]
		if !ok {
			continue
		}
		// secret 占位符表示不修改
		if it.Secret && IsMasked(v) {
			continue
		}
		// 只校验本次修改的值：避免历史配置里已有不规范值（例如带路径的
		// external_url）阻塞用户保存其他设置；用户一旦修改该项仍会严格校验。
		if it.Validate != nil && v != it.Get(s.cfg) {
			if err := it.Validate(v); err != nil {
				return nil, fmt.Errorf("设置 %s 无效: %w", k, err)
			}
		}
	}

	applied := make([]string, 0, len(kv))
	for k, v := range kv {
		it, ok := s.items[k]
		if !ok {
			continue
		}
		if it.Secret && IsMasked(v) {
			continue
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,NOW())
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
			k, v); err != nil {
			return applied, fmt.Errorf("保存设置 %s 失败: %w", k, err)
		}
		it.Set(s.cfg, v)
		applied = append(applied, k)
	}
	return applied, nil
}

// normalizeExternalURL 去除首尾空白和结尾斜杠，避免拼接出 //api/...。
func normalizeExternalURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// validateExternalURL 校验对外访问地址。
//
// 只允许绝对 http/https 地址，且不允许路径、查询参数、片段、用户信息；
// 留空表示回退到当前请求 Host。校验通过后仍会在生成命令时做 shell 转义。
func validateExternalURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	if len(s) > 2048 {
		return fmt.Errorf("地址过长（最多 2048 字符）")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("地址包含控制字符")
		}
	}
	if strings.ContainsAny(s, "'\"") {
		return fmt.Errorf("地址不能包含引号")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("地址格式错误: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("必须以 http:// 或 https:// 开头")
	}
	if u.Host == "" {
		return fmt.Errorf("缺少主机名")
	}
	if u.User != nil {
		return fmt.Errorf("不允许包含用户名或密码")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("不允许包含查询参数或片段")
	}
	if p := strings.TrimSuffix(u.Path, "/"); p != "" {
		return fmt.Errorf("不允许包含路径")
	}
	return nil
}

// View 返回全部可配置项（secret 以占位符返回）。
func (s *Service) View() []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]map[string]any, 0, len(Registry))
	for _, it := range Registry {
		val := it.Get(s.cfg)
		if it.Secret && val != "" {
			val = MaskedSecret
		}
		out = append(out, map[string]any{
			"key": it.Key, "group": it.Group, "label": it.Label,
			"type": it.Type, "secret": it.Secret, "value": val,
		})
	}
	return out
}

// Snapshot 返回当前 Config 的快照副本（用于并发安全的读取）。
func (s *Service) Snapshot() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *s.cfg
}

// NeedsConsulReload 判断变更是否涉及 Consul 连接（需重建客户端）。
func NeedsConsulReload(applied []string) bool {
	for _, k := range applied {
		if strings.HasPrefix(k, "consul.") {
			return true
		}
	}
	return false
}

// NeedsCasdoorReload 判断变更是否涉及 Casdoor/认证。
func NeedsCasdoorReload(applied []string) bool {
	for _, k := range applied {
		if strings.HasPrefix(k, "casdoor.") || k == "auth.provider" {
			return true
		}
	}
	return false
}

// MaskedSecret 密文占位符。
const MaskedSecret = "******"

// IsMasked 判断是否为占位符。
func IsMasked(v string) bool { return v == MaskedSecret }

func b2s(b bool) string { return strconv.FormatBool(b) }

func setInt(dst *int, v string) {
	if n, err := strconv.Atoi(v); err == nil {
		*dst = n
	}
}
