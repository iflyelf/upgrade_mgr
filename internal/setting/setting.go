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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/corsx"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
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
	// Options 可选：type=enum 时的候选值（页面渲染为下拉框）。
	Options []string
	// Restart 为 true 表示该项保存后不会立即生效，需重启服务。
	//
	// 这类配置在启动时被读取并绑定到中间件/连接池等长生命周期对象上，
	// 运行期改内存值不会影响已建立的对象。页面据此给出明确提示，
	// 避免用户改完以为已生效。
	Restart bool
	// ReadOnly 为 true 表示只在页面展示当前值，不接受页面修改。
	//
	// 用于数据库连接这类「鸡生蛋」配置：设置项本身存在数据库里，
	// 连不上库就读不到设置，所以只能由配置文件/环境变量提供。
	// 页面展示出来便于排查「当前到底连的哪个库」。
	ReadOnly bool
	// Note 可选：补充说明（风险提示、生效范围），页面显示在输入框下方。
	Note string
}

// Registry 全部可页面配置项（基础项 + 由 config.AIConfig 自动生成的 AI 项）。
var Registry = append(baseRegistry, aiRegistry()...)

// baseRegistry 手写的基础配置项。
var baseRegistry = []Item{
	// 服务器
	{Key: "server.external_url", Group: "服务器", Label: "对外访问地址 external_url（生成 Agent 安装命令，如 https://upgrade.example.com）", Type: "string",
		Get:      func(c *config.Config) string { return c.Server.ExternalURL },
		Set:      func(c *config.Config, v string) { c.Server.ExternalURL = normalizeExternalURL(v) },
		Validate: validateExternalURL},
	{Key: "server.agent_binary_dir", Group: "服务器",
		Label: "Agent 二进制目录（供 /api/agent/binary/{name} 下载，默认 ./dist）", Type: "string",
		Note: "文件名须为 upgrade-agent-<os>-<arch>[-legacy][.exe]；接入 Windows 主机必须放入 windows-*.exe",
		Get:  func(c *config.Config) string { return c.Server.AgentBinaryDir },
		Set: func(c *config.Config, v string) {
			if t := strings.TrimSpace(v); t != "" {
				c.Server.AgentBinaryDir = t
			}
		}},
	{Key: "server.cors_origins", Group: "服务器",
		Label: "允许跨域的 Origin（逗号分隔，如 https://ops.example.com）", Type: "string",
		Note: "精确匹配 scheme + host + port；不支持 *。保存后 CORS 中间件立即生效。",
		Get:  func(c *config.Config) string { return strings.Join(c.Server.CORSOrigins, ",") },
		Set: func(c *config.Config, v string) {
			origins, _ := corsx.NormalizeOrigins(splitComma(v))
			c.Server.CORSOrigins = origins
		},
		Validate: func(v string) error {
			_, err := corsx.NormalizeOrigins(splitComma(v))
			return err
		}},

	// 以下服务器项在启动时绑定到监听地址/中间件/会话编解码器上，
	// 改内存值不影响已建立的对象，必须重启才生效（Restart=true）。
	{Key: "server.host", Group: "服务器（需重启）", Label: "监听地址", Type: "string",
		Restart: true,
		Get:     func(c *config.Config) string { return c.Server.Host },
		Set: func(c *config.Config, v string) {
			if t := strings.TrimSpace(v); t != "" {
				c.Server.Host = t
			}
		}},
	{Key: "server.port", Group: "服务器（需重启）", Label: "监听端口", Type: "int",
		Restart:  true,
		Get:      func(c *config.Config) string { return strconv.Itoa(c.Server.Port) },
		Set:      func(c *config.Config, v string) { setInt(&c.Server.Port, v) },
		Validate: validateIntRange(1, 65535, "端口")},
	{Key: "server.web_embedded", Group: "服务器（需重启）",
		Label: "启用内嵌前端页面", Type: "bool", Restart: true,
		Get: func(c *config.Config) string { return b2s(c.Server.WebEmbedded) },
		Set: func(c *config.Config, v string) { c.Server.WebEmbedded = v == "true" }},
	{Key: "server.trust_proxy", Group: "服务器（需重启）",
		Label: "信任 X-Forwarded-For / X-Real-IP", Type: "bool", Restart: true,
		Note: "仅在确有反向代理时开启，并同时填写受信代理网段；否则客户端可伪造来源 IP 绕过限流",
		Get:  func(c *config.Config) string { return b2s(c.Server.TrustProxy) },
		Set:  func(c *config.Config, v string) { c.Server.TrustProxy = v == "true" }},
	{Key: "server.trusted_proxies", Group: "服务器（需重启）",
		Label: "受信代理 IP/CIDR（逗号分隔，如 10.0.0.0/8）", Type: "string", Restart: true,
		Note: "XFF 从右往左跳过这些地址，取第一个非受信地址为真实客户端 IP",
		Get:  func(c *config.Config) string { return strings.Join(c.Server.TrustedProxies, ",") },
		Set:  func(c *config.Config, v string) { c.Server.TrustedProxies = splitComma(v) },
		Validate: func(v string) error {
			if strings.TrimSpace(v) == "" {
				return nil
			}
			return extbin.ValidateCIDRs(splitComma(v))
		}},
	{Key: "server.cookie_secure", Group: "服务器（需重启）",
		Label: "会话 Cookie Secure", Type: "enum", Restart: true,
		Options: []string{"auto", "true", "false"},
		Note:    "auto 按 external_url 是否 https 推断；纯 HTTP 部署用 auto 或 false，否则浏览器不会回传 Cookie",
		Get:     func(c *config.Config) string { return c.Server.CookieSecure },
		Set:     func(c *config.Config, v string) { c.Server.CookieSecure = strings.ToLower(strings.TrimSpace(v)) },
		Validate: func(v string) error {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "", "auto", "true", "false":
				return nil
			}
			return fmt.Errorf("只能是 auto / true / false")
		}},
	{Key: "server.metrics_token", Group: "服务器（需重启）",
		Label: "/metrics Bearer 令牌（留空则需登录会话）", Type: "secret", Secret: true, Restart: true,
		Get: func(c *config.Config) string { return c.Server.MetricsToken },
		Set: func(c *config.Config, v string) { c.Server.MetricsToken = strings.TrimSpace(v) }},
	{Key: "server.session_secret", Group: "服务器（需重启）",
		Label: "会话密钥（留空自动生成并持久化；生产建议 32+ 字节）", Type: "secret", Secret: true, Restart: true,
		Note: "⚠️ 修改后所有用户的登录会话立即失效，需重新登录",
		Get:  func(c *config.Config) string { return c.Server.SessionSecret },
		Set: func(c *config.Config, v string) {
			c.Server.SessionSecret = strings.TrimSpace(v)
		},
		Validate: func(v string) error {
			v = strings.TrimSpace(v)
			if v != "" && len(v) < 16 {
				return fmt.Errorf("会话密钥至少 16 字节（建议 32+），或留空由系统生成")
			}
			return nil
		}},

	// Consul（可选数据源）
	{Key: "server.audit_retention_days", Group: "服务器",
		Label: "审计日志保留天数（默认 180，最小 7）", Type: "int",
		Get: func(c *config.Config) string { return strconv.Itoa(c.Server.AuditRetentionDays) },
		Set: func(c *config.Config, v string) { setInt(&c.Server.AuditRetentionDays, v) },
		Validate: func(v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 7 || n > 3650 {
				return fmt.Errorf("审计保留天数应为 7-3650 的整数")
			}
			return nil
		}},

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
	{Key: "consul.timeout", Group: "Consul", Label: "请求超时秒数（默认 10）", Type: "int",
		Get:      func(c *config.Config) string { return strconv.Itoa(c.Consul.Timeout) },
		Set:      func(c *config.Config, v string) { setInt(&c.Consul.Timeout, v) },
		Validate: validateIntRange(1, 300, "Consul 超时"),
		Note:     "保存后重建 Consul 客户端即时生效。"},
	{Key: "consul.concurrency", Group: "Consul",
		Label: "服务实例查询并发数（默认 16）", Type: "int",
		Get:      func(c *config.Config) string { return strconv.Itoa(c.Consul.Concurrency) },
		Set:      func(c *config.Config, v string) { setInt(&c.Consul.Concurrency, v) },
		Validate: validateIntRange(1, 128, "Consul 并发数"),
		Note:     "同步与来源键扫描按服务并行查询；值越大越快，但会增加 Consul 压力。"},
	{Key: "consul.sync_on_start", Group: "Consul",
		Label: "启动时自动同步一次", Type: "bool",
		Get:  func(c *config.Config) string { return b2s(c.Consul.SyncOnStart) },
		Set:  func(c *config.Config, v string) { c.Consul.SyncOnStart = v == "true" },
		Note: "仅影响下次启动；当前进程不会因保存而立即同步（可用主机页「同步 Consul」手动触发）。"},

	// 认证
	{Key: "auth.provider", Group: "认证", Label: "认证方式", Type: "enum",
		Options: []string{"builtin", "casdoor"},
		Get:     func(c *config.Config) string { return c.Auth.Provider },
		Set:     func(c *config.Config, v string) { c.Auth.Provider = strings.ToLower(strings.TrimSpace(v)) },
		Validate: func(v string) error {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "", "builtin", "casdoor":
				return nil
			}
			return fmt.Errorf("只能是 builtin 或 casdoor")
		},
		Restart: true,
		Note:    "认证提供方在启动时构建；切换后需重启服务。切到 casdoor 前请先填好下方 Casdoor 配置，否则重启后无法登录。"},
	{Key: "auth.admin_username", Group: "认证", Label: "内置管理员用户名（provider=builtin）", Type: "string",
		Get:     func(c *config.Config) string { return c.Auth.AdminUsername },
		Set:     func(c *config.Config, v string) { c.Auth.AdminUsername = strings.TrimSpace(v) },
		Restart: true,
		Note:    "仅在启动时用于确保该账号存在；改名不会重命名已有账号，请在「人员组织 → 用户管理」中维护。"},

	// Casdoor（可选 SSO）
	//
	// 全部标记 Restart：认证提供方由 buildAuth 在启动时构建一次并绑定到中间件，
	// 运行期改内存值不会影响已创建的客户端（本服务没有 Casdoor 热重载路径）。
	{Key: "casdoor.endpoint", Group: "Casdoor（需重启）", Label: "后端地址（服务端可达）", Type: "string",
		Get:     func(c *config.Config) string { return c.Casdoor.Endpoint },
		Set:     func(c *config.Config, v string) { c.Casdoor.Endpoint = strings.TrimSpace(v) },
		Restart: true,
		Note:    "保存后需重启服务才生效。"},
	{Key: "casdoor.public_endpoint", Group: "Casdoor（需重启）",
		Label: "浏览器地址（留空复用后端地址）", Type: "string",
		Get:     func(c *config.Config) string { return c.Casdoor.PublicEndpoint },
		Set:     func(c *config.Config, v string) { c.Casdoor.PublicEndpoint = strings.TrimSpace(v) },
		Restart: true},
	{Key: "casdoor.organization", Group: "Casdoor（需重启）", Label: "组织名", Type: "string",
		Get:     func(c *config.Config) string { return c.Casdoor.OrganizationName },
		Set:     func(c *config.Config, v string) { c.Casdoor.OrganizationName = strings.TrimSpace(v) },
		Restart: true},
	{Key: "casdoor.application", Group: "Casdoor（需重启）", Label: "应用名", Type: "string",
		Get:     func(c *config.Config) string { return c.Casdoor.ApplicationName },
		Set:     func(c *config.Config, v string) { c.Casdoor.ApplicationName = strings.TrimSpace(v) },
		Restart: true},
	{Key: "casdoor.client_id", Group: "Casdoor（需重启）", Label: "Client ID", Type: "string",
		Get:     func(c *config.Config) string { return c.Casdoor.ClientId },
		Set:     func(c *config.Config, v string) { c.Casdoor.ClientId = strings.TrimSpace(v) },
		Restart: true},
	{Key: "casdoor.client_secret", Group: "Casdoor（需重启）", Label: "Client Secret", Type: "secret", Secret: true,
		Get:     func(c *config.Config) string { return c.Casdoor.ClientSecret },
		Set:     func(c *config.Config, v string) { c.Casdoor.ClientSecret = v },
		Restart: true},
	{Key: "casdoor.certificate", Group: "Casdoor（需重启）", Label: "证书（可选，预留）", Type: "secret", Secret: true,
		Get:     func(c *config.Config) string { return c.Casdoor.Certificate },
		Set:     func(c *config.Config, v string) { c.Casdoor.Certificate = v },
		Restart: true},
	{Key: "casdoor.redirect_uri", Group: "Casdoor（需重启）",
		Label: "OAuth2 回调地址（留空按 server.external_url + /api/auth/callback 推导）", Type: "string",
		Get: func(c *config.Config) string { return c.Casdoor.RedirectURI },
		Set: func(c *config.Config, v string) { c.Casdoor.RedirectURI = strings.TrimSpace(v) },
		Validate: func(v string) error {
			if strings.TrimSpace(v) == "" {
				return nil
			}
			return validateHTTPURL(v)
		},
		Restart: true,
		Note:    "必须与 Casdoor 应用中登记的回调地址完全一致，否则登录会被拒绝。"},

	// 制品
	{Key: "artifact.dir", Group: "制品", Label: "存储目录", Type: "string",
		Get:     func(c *config.Config) string { return c.Artifact.Dir },
		Set:     func(c *config.Config, v string) { c.Artifact.Dir = v },
		Restart: true,
		Note:    "制品仓储在启动时绑定该目录；修改后需重启。已上传的制品不会自动迁移到新目录。"},
	{Key: "artifact.max_size", Group: "制品",
		Label: "单文件上限字节数（0=不限，默认按 2GB 处理）", Type: "int",
		Get: func(c *config.Config) string { return strconv.FormatInt(c.Artifact.MaxSize, 10) },
		Set: func(c *config.Config, v string) { setInt64(&c.Artifact.MaxSize, v) },
		Validate: func(v string) error {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil || n < 0 {
				return fmt.Errorf("应为 0 或正整数（字节）")
			}
			return nil
		},
		Note: "上传接口每次读取当前值，保存后立即生效。1GB = 1073741824。"},
	{Key: "artifact.max_concurrent_uploads", Group: "制品",
		Label: "并发上传数上限（1-64，默认 4）", Type: "int",
		Get:      func(c *config.Config) string { return strconv.Itoa(c.Artifact.MaxConcurrentUploads) },
		Set:      func(c *config.Config, v string) { setInt(&c.Artifact.MaxConcurrentUploads, v) },
		Validate: validateIntRange(1, 64, "并发上传数"),
		Note:     "每个并发上传会持有一条 PostgreSQL advisory lock 连接；保存后本实例立即热更新上传限流器，并在线把 database.max_open_conns 抬到至少“上传数 + 4”。多副本部署下其他副本约 5 秒内自动同步。"},
	{Key: "artifact.lock_wait", Group: "制品",
		Label: "跨副本上传锁等待时长（1s-24h，默认 30m）", Type: "string",
		Get: func(c *config.Config) string { return c.Artifact.LockWait },
		Set: func(c *config.Config, v string) { c.Artifact.LockWait = strings.TrimSpace(v) },
		Validate: func(v string) error {
			d, err := time.ParseDuration(strings.TrimSpace(v))
			if err != nil || d < time.Second || d > 24*time.Hour {
				return fmt.Errorf("应为 1s-24h 的时长（示例 30m / 1h）")
			}
			return nil
		},
		Note: "等待其他副本上传/删除同一组件制品的最长时间；保存后本实例立即生效，其他副本约 5 秒内自动同步。"},
	{Key: "artifact.storage_type", Group: "制品", Label: "存储后端", Type: "enum",
		Options: []string{"local", "s3", "oss"},
		Get:     func(c *config.Config) string { return c.Artifact.StorageType },
		Set: func(c *config.Config, v string) {
			c.Artifact.StorageType = strings.ToLower(strings.TrimSpace(v))
		},
		Validate: func(v string) error {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "", "local", "s3", "oss":
				return nil
			}
			return fmt.Errorf("只能是 local / s3 / oss")
		},
		Restart: true,
		Note:    "切换后需重启。改为 s3/oss 时必须同时填好下方对应参数，否则启动时初始化失败会自动降级为 local。"},
	{Key: "artifact.s3_endpoint", Group: "制品存储后端（需重启）", Label: "S3/MinIO 端点（storage_type=s3）", Type: "string",
		Get:     func(c *config.Config) string { return c.Artifact.S3Endpoint },
		Set:     func(c *config.Config, v string) { c.Artifact.S3Endpoint = strings.TrimSpace(v) },
		Restart: true,
		Note:    "如 http://minio:9000；留空则无法使用 s3 后端"},
	{Key: "artifact.s3_region", Group: "制品存储后端（需重启）", Label: "S3 区域", Type: "string",
		Get:     func(c *config.Config) string { return c.Artifact.S3Region },
		Set:     func(c *config.Config, v string) { c.Artifact.S3Region = strings.TrimSpace(v) },
		Restart: true},
	{Key: "artifact.s3_bucket", Group: "制品存储后端（需重启）", Label: "S3 存储桶", Type: "string",
		Get:     func(c *config.Config) string { return c.Artifact.S3Bucket },
		Set:     func(c *config.Config, v string) { c.Artifact.S3Bucket = strings.TrimSpace(v) },
		Restart: true},
	{Key: "artifact.s3_access_key", Group: "制品存储后端（需重启）", Label: "S3 Access Key", Type: "string",
		Get:     func(c *config.Config) string { return c.Artifact.S3AccessKey },
		Set:     func(c *config.Config, v string) { c.Artifact.S3AccessKey = strings.TrimSpace(v) },
		Restart: true},
	{Key: "artifact.s3_secret_key", Group: "制品存储后端（需重启）", Label: "S3 Secret Key", Type: "secret", Secret: true,
		Get:     func(c *config.Config) string { return c.Artifact.S3SecretKey },
		Set:     func(c *config.Config, v string) { c.Artifact.S3SecretKey = v },
		Restart: true},
	{Key: "artifact.s3_use_ssl", Group: "制品存储后端（需重启）", Label: "S3 使用 HTTPS", Type: "bool",
		Get:     func(c *config.Config) string { return b2s(c.Artifact.S3UseSSL) },
		Set:     func(c *config.Config, v string) { c.Artifact.S3UseSSL = v == "true" },
		Restart: true},
	{Key: "artifact.oss_endpoint", Group: "制品存储后端（需重启）", Label: "阿里云 OSS 端点（storage_type=oss）", Type: "string",
		Get:     func(c *config.Config) string { return c.Artifact.OSSEndpoint },
		Set:     func(c *config.Config, v string) { c.Artifact.OSSEndpoint = strings.TrimSpace(v) },
		Restart: true,
		Note:    "如 oss-cn-hangzhou.aliyuncs.com"},
	{Key: "artifact.oss_bucket", Group: "制品存储后端（需重启）", Label: "OSS 存储桶", Type: "string",
		Get:     func(c *config.Config) string { return c.Artifact.OSSBucket },
		Set:     func(c *config.Config, v string) { c.Artifact.OSSBucket = strings.TrimSpace(v) },
		Restart: true},
	{Key: "artifact.oss_access_key", Group: "制品存储后端（需重启）", Label: "OSS Access Key", Type: "string",
		Get:     func(c *config.Config) string { return c.Artifact.OSSAccessKey },
		Set:     func(c *config.Config, v string) { c.Artifact.OSSAccessKey = strings.TrimSpace(v) },
		Restart: true},
	{Key: "artifact.oss_secret_key", Group: "制品存储后端（需重启）", Label: "OSS Secret Key", Type: "secret", Secret: true,
		Get:     func(c *config.Config) string { return c.Artifact.OSSSecretKey },
		Set:     func(c *config.Config, v string) { c.Artifact.OSSSecretKey = v },
		Restart: true},

	// Agent / 指令
	{Key: "agent.poll_hold", Group: "Agent", Label: "长轮询挂起(秒)", Type: "int",
		Get: func(c *config.Config) string { return strconv.Itoa(c.Agent.PollHold) },
		Set: func(c *config.Config, v string) { setInt(&c.Agent.PollHold, v) }},
	{Key: "agent.lease_max", Group: "Agent", Label: "维护窗口上限(1h)", Type: "string",
		Get:      func(c *config.Config) string { return c.Agent.LeaseMax },
		Set:      func(c *config.Config, v string) { c.Agent.LeaseMax = strings.TrimSpace(v) },
		Validate: validateDuration(true)},
	{Key: "agent.require_enroll_token", Group: "Agent",
		Label: "强制要求注册令牌（生产环境强烈建议开启）", Type: "bool",
		Get:  func(c *config.Config) string { return b2s(c.Agent.RequireEnrollToken) },
		Set:  func(c *config.Config, v string) { c.Agent.RequireEnrollToken = v == "true" },
		Note: "关闭后任何能访问本服务的主机都可自行注册，仅限可信内网使用。"},
	{Key: "agent.enroll_token_ttl", Group: "Agent",
		Label: "注册令牌有效期（如 15m）", Type: "string",
		Get:      func(c *config.Config) string { return c.Agent.EnrollTokenTTL },
		Set:      func(c *config.Config, v string) { c.Agent.EnrollTokenTTL = strings.TrimSpace(v) },
		Validate: validateDuration(true)},
	{Key: "agent.clock_skew", Group: "Agent",
		Label: "指令签名允许的时钟偏移（如 5m）", Type: "string",
		Get: func(c *config.Config) string { return c.Agent.ClockSkew },
		Set: func(c *config.Config, v string) { c.Agent.ClockSkew = strings.TrimSpace(v) },
		Validate: func(v string) error {
			d, err := time.ParseDuration(strings.TrimSpace(v))
			if err != nil || d < 30*time.Second || d > 30*time.Minute {
				return fmt.Errorf("时钟偏移应为 30s-30m（建议 5m）")
			}
			return nil
		},
		Note: "服务端随心跳下发，Agent 最迟一个心跳周期内生效并持久化；窗口越大，重放防护越弱。"},
	{Key: "agent.allow_exec_mode", Group: "Agent", Label: "允许 exec/script 模式与命令任务", Type: "bool",
		Get: func(c *config.Config) string { return b2s(c.Agent.AllowExecMode) },
		Set: func(c *config.Config, v string) { c.Agent.AllowExecMode = v == "true" }},
	{Key: "agent.auto_update", Group: "Agent", Label: "自动升级旧 Agent（下载新二进制并重启 Agent）", Type: "bool",
		Get: func(c *config.Config) string { return b2s(c.Agent.AutoUpdate) },
		Set: func(c *config.Config, v string) { c.Agent.AutoUpdate = v == "true" }},
	{Key: "agent.auto_update_percent", Group: "Agent",
		Label: "自动升级灰度比例（0-100，按 agent_id 稳定哈希选择）", Type: "int",
		Get: func(c *config.Config) string { return strconv.Itoa(c.Agent.AutoUpdatePercent) },
		Set: func(c *config.Config, v string) { setInt(&c.Agent.AutoUpdatePercent, v) },
		Validate: func(v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 || n > 100 {
				return fmt.Errorf("灰度比例应为 0-100 的整数")
			}
			return nil
		}},
	{Key: "agent.task_owner_only", Group: "Agent",
		Label: "任务仅创建者可操作（默认开启；团队 owner_only 可为团队成员放开）", Type: "bool",
		Get: func(c *config.Config) string { return b2s(c.Agent.TaskOwnerOnly) },
		Set: func(c *config.Config, v string) { c.Agent.TaskOwnerOnly = v == "true" }},
	{Key: "agent.offline_after", Group: "Agent",
		Label: "心跳超时阈值（如 1m），超过此时长视为离线", Type: "string",
		Get: func(c *config.Config) string { return c.Agent.OfflineAfter },
		Set: func(c *config.Config, v string) { c.Agent.OfflineAfter = strings.TrimSpace(v) },
		Validate: func(v string) error {
			d, err := time.ParseDuration(strings.TrimSpace(v))
			if err != nil || d < 30*time.Second || d > 30*time.Minute {
				return fmt.Errorf("心跳超时阈值应为 30s-30m（建议 1m，Agent 心跳周期为 30s）")
			}
			return nil
		},
		Note: "Agent 心跳周期 30s；默认 1m 允许 2 次心跳丢失。主机列表、版本统计、指标都按此阈值判定离线。"},

	// Task / 任务创建
	{Key: "task.host_selector_fields", Group: "Task",
		Label: "任务可选主机字段（逗号分隔，如 app_code,biz_group,os_family）", Type: "string",
		Get:  func(c *config.Config) string { return c.Task.HostSelectorFields },
		Set:  func(c *config.Config, v string) { c.Task.HostSelectorFields = strings.TrimSpace(v) },
		Note: "为空时禁用字段筛选（仅保留 IP 段）；可选字段：app_code、biz_group、os_family、os_type、account、department、vendor、arch、host_type。"},

	// FlyIAM 集成（与 consul_mgr 复用同一部署）
	{Key: "flyiam.endpoint", Group: "FlyIAM",
		Label: "FlyIAM 后端地址（如 http://flyiam.internal:8000）", Type: "string",
		Get: func(c *config.Config) string { return c.FlyIAM.Endpoint },
		Set: func(c *config.Config, v string) { c.FlyIAM.Endpoint = strings.TrimRight(strings.TrimSpace(v), "/") },
		Validate: func(v string) error {
			v = strings.TrimSpace(v)
			if v == "" {
				return nil
			}
			return validateHTTPURL(v)
		}},
	{Key: "flyiam.service_token", Group: "FlyIAM",
		Label: "服务令牌（X-Service-Token）", Type: "secret", Secret: true,
		Get: func(c *config.Config) string { return c.FlyIAM.ServiceToken },
		Set: func(c *config.Config, v string) { c.FlyIAM.ServiceToken = strings.TrimSpace(v) }},

	// consul_mgr 对接：一次拉取多个服务组（各自独立的 Consul）的主机清单
	{Key: "consul_mgr.endpoint", Group: "consul_mgr 对接",
		Label: "consul_mgr 后端地址（如 http://consul-mgr.internal:8888；留空表示不启用）", Type: "string",
		Get: func(c *config.Config) string { return c.ConsulMgr.Endpoint },
		Set: func(c *config.Config, v string) {
			c.ConsulMgr.Endpoint = strings.TrimRight(strings.TrimSpace(v), "/")
		},
		Validate: func(v string) error {
			v = strings.TrimSpace(v)
			if v == "" {
				return nil
			}
			return validateHTTPURL(v)
		}},
	{Key: "consul_mgr.service_token", Group: "consul_mgr 对接",
		Label: "服务令牌（需与 consul_mgr「设置 → 服务间 API」的入站令牌一致）",
		Type:  "secret", Secret: true,
		Get: func(c *config.Config) string { return c.ConsulMgr.ServiceToken },
		Set: func(c *config.Config, v string) { c.ConsulMgr.ServiceToken = strings.TrimSpace(v) }},
	{Key: "consul_mgr.timeout", Group: "consul_mgr 对接",
		Label: "请求超时秒数（默认 30；主机多时适当放大）", Type: "int",
		Get: func(c *config.Config) string { return strconv.Itoa(c.ConsulMgr.Timeout) },
		Set: func(c *config.Config, v string) { setInt(&c.ConsulMgr.Timeout, v) },
		Validate: func(v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 5 || n > 600 {
				return fmt.Errorf("超时应为 5-600 秒")
			}
			return nil
		}},
	// 服务组不在设置里配置：同步时在主机页的下拉中选择（候选来自
	// GET /api/consul-mgr/groups），避免「页面能选、配置又写死」两处语义冲突。

	// 外部文件代缓存（供老系统通过 HTTP 获取公网文件）
	{Key: "server.external_binaries", Group: "外部文件代缓存",
		Label: "白名单（每行 name=地址；脚本用 script: 前缀，如 windows-exporter-install.ps1=script:https://...）",
		Type:  "text",
		Get:   func(c *config.Config) string { return extbin.FormatText(c.Server.ExternalBinaries) },
		Set: func(c *config.Config, v string) {
			if m, err := extbin.ParseText(v); err == nil {
				c.Server.ExternalBinaries = m
			}
		},
		Validate: func(v string) error {
			// 仅校验名称与地址格式；域名白名单校验放在 Apply 中，
			// 因为需要读取"本次提交的 hosts"或当前配置（Item.Validate 拿不到 Config）。
			m, err := extbin.ParseText(v)
			if err != nil {
				return err
			}
			_, err = extbin.Parse(m)
			return err
		}},
	{Key: "server.external_binary_hosts", Group: "外部文件代缓存",
		Label: "允许的上游域名（逗号分隔；留空用默认 github.com/raw.githubusercontent.com/proxy.xiaonuo.live）",
		Type:  "string",
		Get:   func(c *config.Config) string { return strings.Join(c.Server.ExternalBinaryHosts, ",") },
		Set: func(c *config.Config, v string) {
			if strings.TrimSpace(v) == "" {
				c.Server.ExternalBinaryHosts = nil
				return
			}
			c.Server.ExternalBinaryHosts = splitComma(v)
		}},
	{Key: "server.external_binary_proxy", Group: "外部文件代缓存",
		Label:    "加速代理地址（留空直连；默认 https://proxy.xiaonuo.live/proxy?url=）",
		Type:     "string",
		Get:      func(c *config.Config) string { return c.Server.ExternalBinaryProxy },
		Set:      func(c *config.Config, v string) { c.Server.ExternalBinaryProxy = strings.TrimSpace(v) },
		Validate: validateHTTPURL,
		Note:     "默认先走加速代理拉取 GitHub/raw，失败再直连兜底；留空表示完全直连。"},
	{Key: "server.external_binary_proxy_hosts", Group: "外部文件代缓存",
		Label: "走加速代理的上游域名（逗号分隔；留空不启用代理）",
		Type:  "string",
		Get:   func(c *config.Config) string { return strings.Join(c.Server.ExternalBinaryProxyHosts, ",") },
		Set: func(c *config.Config, v string) {
			if strings.TrimSpace(v) == "" {
				c.Server.ExternalBinaryProxyHosts = nil
				return
			}
			c.Server.ExternalBinaryProxyHosts = splitComma(v)
		},
		Note: "默认 github.com/raw.githubusercontent.com 及 GitHub Release 资产域名；内网镜像不要加入。"},
	{Key: "server.external_binary_allow_private", Group: "外部文件代缓存",
		Label: "允许访问内网/环回地址（内网镜像站需要；建议优先用“放行网段”精确放开）", Type: "bool",
		Get: func(c *config.Config) string { return b2s(c.Server.ExternalBinaryAllowPrivate) },
		Set: func(c *config.Config, v string) { c.Server.ExternalBinaryAllowPrivate = v == "true" }},
	{Key: "server.external_binary_allow_cidrs", Group: "外部文件代缓存",
		Label: "放行网段（逗号分隔，如 10.0.0.0/8,192.168.1.10；仅这些内网地址可代下载）",
		Type:  "string",
		Get:   func(c *config.Config) string { return strings.Join(c.Server.ExternalBinaryAllowCIDRs, ",") },
		Set:   func(c *config.Config, v string) { c.Server.ExternalBinaryAllowCIDRs = splitComma(v) },
		Validate: func(v string) error {
			if v == "" {
				return nil
			}
			return extbin.ValidateCIDRs(splitComma(v))
		}},
	{Key: "server.external_binary_ttl", Group: "外部文件代缓存",
		Label: "缓存有效期（如 24h；留空默认 24h）", Type: "string",
		Get: func(c *config.Config) string { return c.Server.ExternalBinaryTTL },
		Set: func(c *config.Config, v string) { c.Server.ExternalBinaryTTL = strings.TrimSpace(v) },
		Validate: func(v string) error {
			v = strings.TrimSpace(v)
			if v == "" {
				return nil
			}
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return fmt.Errorf("无效时长（示例 24h / 30m）")
			}
			return nil
		}},

	// Redis 缓存（可选；单点/集群/哨兵）
	{Key: "redis.enabled", Group: "Redis 连接配置", Label: "启用 Redis 缓存（读写失败自动降级，不阻塞主流程）", Type: "bool",
		Get: func(c *config.Config) string { return b2s(c.Redis.Enabled) },
		Set: func(c *config.Config, v string) { c.Redis.Enabled = v == "true" }},
	{Key: "redis.mode", Group: "Redis 连接配置", Label: "连接模式", Type: "enum",
		Options: []string{"single", "cluster", "sentinel"},
		Get:     func(c *config.Config) string { return c.Redis.Mode },
		Set:     func(c *config.Config, v string) { c.Redis.Mode = strings.ToLower(strings.TrimSpace(v)) },
		Validate: func(v string) error {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "", "single", "cluster", "sentinel":
				return nil
			}
			return fmt.Errorf("只能是 single / cluster / sentinel")
		}},
	{Key: "redis.hosts", Group: "Redis 连接配置",
		Label: "地址列表（逗号分隔；单点可只填 host:port；哨兵填哨兵地址；集群填种子节点）", Type: "string",
		Get: func(c *config.Config) string { return strings.Join(c.Redis.Hosts, ",") },
		Set: func(c *config.Config, v string) {
			if strings.TrimSpace(v) == "" {
				c.Redis.Hosts = nil
				return
			}
			c.Redis.Hosts = splitComma(v)
		},
		Validate: func(v string) error {
			for _, a := range splitComma(v) {
				if strings.ContainsAny(a, " \t\r\n") {
					return fmt.Errorf("地址 %q 含空白字符", a)
				}
			}
			return nil
		}},
	{Key: "redis.master_name", Group: "Redis 连接配置", Label: "哨兵主节点名（mode=sentinel 必填）", Type: "string",
		Get: func(c *config.Config) string { return c.Redis.MasterName },
		Set: func(c *config.Config, v string) { c.Redis.MasterName = strings.TrimSpace(v) }},
	{Key: "redis.password", Group: "Redis 连接配置", Label: "密码（留空表示无密码）", Type: "secret", Secret: true,
		Get: func(c *config.Config) string { return c.Redis.Password },
		Set: func(c *config.Config, v string) { c.Redis.Password = v }},
	{Key: "redis.db", Group: "Redis 连接配置", Label: "DB 索引（集群固定 0；超出服务器范围会自动回退并提示）", Type: "int",
		Get: func(c *config.Config) string { return strconv.Itoa(c.Redis.DB) },
		Set: func(c *config.Config, v string) { setInt(&c.Redis.DB, v) }},
	{Key: "redis.ttl", Group: "Redis 连接配置", Label: "只读列表缓存秒数（默认 10，上限 30；写操作会立即失效）", Type: "int",
		Get: func(c *config.Config) string { return strconv.Itoa(c.Redis.TTL) },
		Set: func(c *config.Config, v string) { setInt(&c.Redis.TTL, v) }},
	{Key: "redis.hosts_ttl", Group: "Redis 连接配置",
		Label: "主机列表缓存秒数（0=同上；上限 300。几万台规模时可调到 60-120）", Type: "int",
		Get: func(c *config.Config) string { return strconv.Itoa(c.Redis.HostsTTL) },
		Set: func(c *config.Config, v string) { setInt(&c.Redis.HostsTTL, v) }},
	{Key: "redis.prefix", Group: "Redis 连接配置", Label: "键前缀（默认 upgrade_mgr:，多环境共用实例时用于隔离）", Type: "string",
		Get: func(c *config.Config) string { return c.Redis.Prefix },
		Set: func(c *config.Config, v string) { c.Redis.Prefix = strings.TrimSpace(v) }},

	// 数据库（只读展示）
	//
	// 设置项本身存在数据库的 app_settings 表里：启动顺序是「先连库 → 再读设置」，
	// 所以数据库连接参数不可能由页面提供（连不上库就读不到设置）。
	// 这里只展示当前生效值，便于确认「服务到底连的哪个库」，修改仍走配置文件/环境变量。
	{Key: "database.host", Group: "数据库（只读，改配置文件后重启）", Label: "主机", Type: "string",
		ReadOnly: true, Note: "由 config.yaml 的 database.host 或环境变量 DB_HOST 提供",
		Get: func(c *config.Config) string { return c.Database.Host },
		Set: func(c *config.Config, v string) {}},
	{Key: "database.port", Group: "数据库（只读，改配置文件后重启）", Label: "端口", Type: "int",
		ReadOnly: true,
		Get:      func(c *config.Config) string { return strconv.Itoa(c.Database.Port) },
		Set:      func(c *config.Config, v string) {}},
	{Key: "database.user", Group: "数据库（只读，改配置文件后重启）", Label: "用户名", Type: "string",
		ReadOnly: true,
		Get:      func(c *config.Config) string { return c.Database.User },
		Set:      func(c *config.Config, v string) {}},
	{Key: "database.dbname", Group: "数据库（只读，改配置文件后重启）", Label: "库名", Type: "string",
		ReadOnly: true,
		Get:      func(c *config.Config) string { return c.Database.DBName },
		Set:      func(c *config.Config, v string) {}},
	{Key: "database.sslmode", Group: "数据库（只读，改配置文件后重启）", Label: "SSL 模式", Type: "string",
		ReadOnly: true,
		Get:      func(c *config.Config) string { return c.Database.SSLMode },
		Set:      func(c *config.Config, v string) {}},
	{Key: "database.max_open_conns", Group: "数据库（只读，改配置文件后重启）",
		Label: "最大连接数", Type: "int", ReadOnly: true,
		Get:  func(c *config.Config) string { return strconv.Itoa(c.Database.MaxOpenConns) },
		Set:  func(c *config.Config, v string) {},
		Note: "运行时按 artifact.max_concurrent_uploads + 4 自动抬高；页面显示的是配置文件基数（每个上传持有一条 advisory lock 连接）。"},
	{Key: "database.max_idle_conns", Group: "数据库（只读，改配置文件后重启）",
		Label: "最大空闲连接数", Type: "int", ReadOnly: true,
		Get: func(c *config.Config) string { return strconv.Itoa(c.Database.MaxIdleConns) },
		Set: func(c *config.Config, v string) {}},
	{Key: "database.conn_max_lifetime", Group: "数据库（只读，改配置文件后重启）",
		Label: "连接最大存活秒数", Type: "int", ReadOnly: true,
		Get: func(c *config.Config) string { return strconv.Itoa(c.Database.ConnMaxLifetime) },
		Set: func(c *config.Config, v string) {}},

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
	// base 为「默认值 → 配置文件 → 环境变量」计算出的基线（加载页面设置之前的值），
	// 用于「恢复为配置文件值」与来源标注；defaults 为内置默认值。
	base     *config.Config
	defaults *config.Config
	// pageKeys 当前已在页面保存（app_settings 中存在）的键。
	pageKeys map[string]bool
}

// 设置来源：页面保存 / 配置文件或环境变量 / 内置默认值。
const (
	SourcePage      = "page"
	SourceFileOrEnv = "file_or_env"
	SourceDefault   = "default"
)

// NewService 创建设置服务。cfg 此时应为配置文件+环境变量加载后的结果。
func NewService(db *sql.DB, cfg *config.Config) *Service {
	s := &Service{db: db, cfg: cfg, items: registryMap, pageKeys: map[string]bool{}}
	if cfg != nil {
		s.base = cloneConfig(cfg)
	}
	s.defaults = config.Default()
	return s
}

// cloneConfig 复制配置（map 字段深拷贝；slice 字段在 Set 中总是整体替换，可共享）。
func cloneConfig(c *config.Config) *config.Config {
	cp := *c
	if c.Server.ExternalBinaries != nil {
		m := make(map[string]string, len(c.Server.ExternalBinaries))
		for k, v := range c.Server.ExternalBinaries {
			m[k] = v
		}
		cp.Server.ExternalBinaries = m
	}
	return &cp
}

// sourceOf 返回某项当前值的来源（调用方需持有读锁）。
func (s *Service) sourceOf(it Item) string {
	if s.pageKeys[it.Key] {
		return SourcePage
	}
	if s.base != nil && s.defaults != nil && it.Get(s.base) != it.Get(s.defaults) {
		return SourceFileOrEnv
	}
	return SourceDefault
}

// BaseValue 返回某项在配置文件/环境变量/默认值中的值（secret 脱敏）。
func (s *Service) BaseValue(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	it, ok := s.items[key]
	if !ok || s.base == nil {
		return "", false
	}
	v := it.Get(s.base)
	if it.Secret && v != "" {
		v = MaskedSecret
	}
	return v, true
}

// Reset 删除页面保存的值，恢复为配置文件/环境变量/默认值（立即生效）。
func (s *Service) Reset(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[key]
	if !ok {
		return fmt.Errorf("未知设置项 %s", key)
	}
	if it.ReadOnly {
		return fmt.Errorf("设置 %s 为只读项，无需恢复", key)
	}
	if s.db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM app_settings WHERE key=$1`, key); err != nil {
		return fmt.Errorf("删除页面设置 %s 失败: %w", key, err)
	}
	if s.base != nil {
		it.Set(s.cfg, it.Get(s.base))
	}
	delete(s.pageKeys, key)
	return nil
}

// SyncFromDB 用数据库中的页面设置同步内存（多副本场景）：
// 新增/修改的值写回内存；已被其他副本「恢复为配置文件值」（行已删除）的键回退到基线。
// 只处理 keys 范围内的已注册、非只读项；返回实际变化的键。
func (s *Service) SyncFromDB(ctx context.Context, keys []string) ([]string, error) {
	vals, err := s.ReadValues(ctx, keys...)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := []string{}
	for _, k := range keys {
		it, ok := s.items[k]
		if !ok || it.ReadOnly {
			continue
		}
		v, inDB := vals[k]
		if !inDB {
			if s.pageKeys[k] {
				delete(s.pageKeys, k)
				if s.base != nil && it.Get(s.cfg) != it.Get(s.base) {
					it.Set(s.cfg, it.Get(s.base))
				}
				changed = append(changed, k)
			}
			continue
		}
		s.pageKeys[k] = true
		if it.Secret && IsMasked(v) || v == it.Get(s.cfg) {
			continue
		}
		if it.Validate != nil && it.Validate(v) != nil {
			continue
		}
		it.Set(s.cfg, v)
		changed = append(changed, k)
	}
	return changed, nil
}

// KeysWithPrefix 返回已注册的、指定前缀的可写设置键。
func KeysWithPrefix(prefix string) []string {
	out := []string{}
	for _, it := range Registry {
		if !it.ReadOnly && strings.HasPrefix(it.Key, prefix) {
			out = append(out, it.Key)
		}
	}
	return out
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
			if !it.ReadOnly {
				s.pageKeys[k] = true
			}
		}
	}
	return rows.Err()
}

// Apply 写入 DB 并即时应用到内存 Config。返回实际生效的 key 列表。
func (s *Service) Apply(ctx context.Context, kv map[string]string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 白名单与域名白名单可能同批提交：先用"本次提交值（否则当前值）"做域名校验，
	// 避免保存自定义镜像域名时被旧 hosts 误拒。
	if binRaw, ok := kv["server.external_binaries"]; ok && !(s.items["server.external_binaries"].Secret && IsMasked(binRaw)) {
		hosts := s.cfg.Server.ExternalBinaryHosts
		if hv, ok2 := kv["server.external_binary_hosts"]; ok2 {
			hosts = splitComma(hv)
		}
		m, perr := extbin.ParseText(binRaw)
		if perr != nil {
			return nil, fmt.Errorf("设置 server.external_binaries 无效: %w", perr)
		}
		if verr := extbin.ValidateConfig(m, hosts); verr != nil {
			return nil, fmt.Errorf("设置 server.external_binaries 无效: %w", verr)
		}
	}

	// 先整体校验，再落库：避免某一项非法时，其他项已经部分生效。
	for k, v := range kv {
		it, ok := s.items[k]
		if !ok {
			continue
		}
		// 只读项（数据库连接参数）：页面会禁用输入框，但不能只靠前端。
		// 这里显式拒绝，避免直接调 API 把无效值写进 app_settings，
		// 下次启动 Load 时又被套用到 Config 上。
		if it.ReadOnly {
			if v != it.Get(s.cfg) {
				return nil, fmt.Errorf("设置 %s 为只读项，请修改配置文件或环境变量后重启", k)
			}
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

	// 先收集需要写入的项，再单事务提交：任一项失败则整批回滚，
	// 避免页面一次保存多个设置时出现“部分生效”的不一致状态。
	type pendingSetting struct {
		key  string
		val  string
		item Item
	}
	pending := make([]pendingSetting, 0, len(kv))
	for k, v := range kv {
		it, ok := s.items[k]
		if !ok {
			continue
		}
		// 只读项不落库：上面已确认值未变，这里直接跳过，
		// 保持 app_settings 里不出现数据库连接参数。
		if it.ReadOnly {
			continue
		}
		if it.Secret && IsMasked(v) {
			continue
		}
		pending = append(pending, pendingSetting{key: k, val: v, item: it})
	}
	// 固定写入顺序，便于测试与排查；事务内顺序不影响最终结果。
	sort.Slice(pending, func(i, j int) bool { return pending[i].key < pending[j].key })
	applied := make([]string, 0, len(pending))
	if len(pending) == 0 {
		return applied, nil
	}
	if s.db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开始保存设置事务失败: %w", err)
	}
	defer tx.Rollback()
	for _, p := range pending {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,NOW())
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
			p.key, p.val); err != nil {
			return nil, fmt.Errorf("保存设置 %s 失败: %w", p.key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交设置失败: %w", err)
	}
	// 事务提交成功后再一次性更新内存（仍持有 s.mu 写锁，
	// 期间 Snapshot 读到的是提交前的完整旧值或提交后的完整新值）。
	for _, p := range pending {
		p.item.Set(s.cfg, p.val)
		if s.pageKeys != nil {
			s.pageKeys[p.key] = true
		}
		applied = append(applied, p.key)
	}
	return applied, nil
}

// validateHTTPURL 校验 http/https 绝对地址（用于服务间地址）。
func validateHTTPURL(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("地址格式错误: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("必须以 http:// 或 https:// 开头")
	}
	if u.Host == "" {
		return fmt.Errorf("缺少主机名")
	}
	return nil
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
			"options": it.Options,
			// readonly/restart/note 供页面区分「可改且立即生效」
			// 「可改但需重启」「只读展示」三种情况。
			"readonly": it.ReadOnly, "restart": it.Restart, "note": it.Note,
			// source：page（页面保存，优先级最高）/ file_or_env / default。
			"source": s.sourceOf(it),
		})
	}
	return out
}

// ReadValues 读取指定键在数据库中的当前值；未出现的键不放入返回 map。
//
// 用于多副本运行时同步：不修改内存配置，也不写数据库。
func (s *Service) ReadValues(ctx context.Context, keys ...string) (map[string]string, error) {
	out := map[string]string{}
	if s == nil || s.db == nil || len(keys) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = k
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM app_settings WHERE key IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ApplyRuntime 仅把给定值写回内存 Config，不写数据库。
//
// 用于多副本运行时同步：某副本保存设置后，其他副本轮询到 DB 变化时调用。
// 只接受已注册、非只读、校验通过且值确实变化的项；返回实际更新的键。
func (s *Service) ApplyRuntime(kv map[string]string) []string {
	if s == nil || len(kv) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	applied := make([]string, 0, len(kv))
	for k, v := range kv {
		it, ok := s.items[k]
		if !ok || it.ReadOnly {
			continue
		}
		if it.Secret && IsMasked(v) {
			continue
		}
		if v == it.Get(s.cfg) {
			continue
		}
		if it.Validate != nil {
			if err := it.Validate(v); err != nil {
				continue
			}
		}
		it.Set(s.cfg, v)
		applied = append(applied, k)
	}
	return applied
}

// Snapshot 返回当前 Config 的快照副本（用于并发安全的读取）。
func (s *Service) Snapshot() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := *s.cfg
	// map 是引用类型：浅拷贝后并发读写会触发 data race，
	// 这里克隆使用中的 map 字段（外部文件白名单）。
	if s.cfg.Server.ExternalBinaries != nil {
		m := make(map[string]string, len(s.cfg.Server.ExternalBinaries))
		for k, v := range s.cfg.Server.ExternalBinaries {
			m[k] = v
		}
		snap.Server.ExternalBinaries = m
	}
	return snap
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

// NeedsCacheReload 判断变更是否涉及 Redis（需重建客户端）。
func NeedsCacheReload(applied []string) bool {
	for _, k := range applied {
		if strings.HasPrefix(k, "redis.") {
			return true
		}
	}
	return false
}

// NeedsArtifactReload 判断变更是否涉及制品上传并发/锁等待（可热更新）。
func NeedsArtifactReload(applied []string) bool {
	for _, k := range applied {
		switch k {
		case "artifact.max_concurrent_uploads", "artifact.lock_wait":
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

// splitComma 按逗号拆分并去空白。
func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func setInt(dst *int, v string) {
	if n, err := strconv.Atoi(v); err == nil {
		*dst = n
	}
}

// setInt64 同 setInt，用于 int64 字段（如制品大小上限）。
func setInt64(dst *int64, v string) {
	if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
		*dst = n
	}
}

// validateIntRange 生成「整数且在 [min,max] 内」的校验函数。
//
// name 用于错误提示，便于用户定位是哪一项填错。
func validateIntRange(min, max int, name string) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < min || n > max {
			return fmt.Errorf("%s应为 %d-%d 的整数", name, min, max)
		}
		return nil
	}
}

// validateDuration 校验 Go 时长字符串（如 15m / 1h）；allowEmpty 时空值合法。
func validateDuration(allowEmpty bool) func(string) error {
	return func(v string) error {
		v = strings.TrimSpace(v)
		if v == "" {
			if allowEmpty {
				return nil
			}
			return fmt.Errorf("不能为空（示例 15m / 1h）")
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("无效时长（示例 15m / 1h）")
		}
		return nil
	}
}
