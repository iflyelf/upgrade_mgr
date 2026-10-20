// Package config 定义 upgrade-mgr 控制台配置。
//
// 分层原则：
//   - 必填：数据库、监听地址（缺失即启动失败）；
//   - 可选：Consul / Casdoor / Redis / 制品目录（缺失仅告警，功能降级，不阻塞启动）。
//
// 取值优先级：环境变量 > 配置文件默认；运行期「页面设置」(app_settings 表) 会在
// 启动加载后覆盖为最高优先级（DB 优先 / env 兜底），见 internal/setting。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 应用配置（各段指针语义：svc 持有 *Config，页面设置直接写回即时生效）。
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Auth     AuthConfig     `yaml:"auth"`
	Consul   ConsulConfig   `yaml:"consul"`
	Casdoor  CasdoorConfig  `yaml:"casdoor"`
	Redis    RedisConfig    `yaml:"redis"`
	Artifact ArtifactConfig `yaml:"artifact"`
	Agent    AgentConfig    `yaml:"agent"`
	Log      LogConfig      `yaml:"log"`
}

// ServerConfig 监听与前端。
type ServerConfig struct {
	Host          string   `yaml:"host"`
	Port          int      `yaml:"port"`
	WebEmbedded   bool     `yaml:"web_embedded"`
	CORSOrigins   []string `yaml:"cors_origins"`
	SessionSecret string   `yaml:"session_secret"` // 内置登录会话签名密钥，空则随机
	// CookieSecure 会话 Cookie 的 Secure 属性：auto（按 ExternalURL 是否 https 推断）/true/false。
	// 生产环境务必经 HTTPS 暴露，否则会话令牌可被明文窃取。
	CookieSecure string `yaml:"cookie_secure"`
	ExternalURL  string `yaml:"external_url"` // 对外可达地址，用于生成 Agent 安装命令
	// TrustProxy 是否信任 X-Forwarded-For / X-Real-IP 头。
	//
	// 安全要点：仅当服务确实部署在可信反向代理（Nginx/LB）之后才可开启。
	// 若服务直接暴露而开启此项，攻击者可伪造该头绕过基于 IP 的限流与审计。
	// 默认 false（只信任 TCP 对端地址）。
	TrustProxy bool `yaml:"trust_proxy"`
	// TrustedProxies 受信代理 CIDR/IP 列表（如 10.0.0.0/8、127.0.0.1）。
	// TrustProxy=true 时从 X-Forwarded-For 右侧向左跳过这些地址，取第一个非受信地址为客户端 IP；
	// 列表为空时取 XFF 最右值（即直连代理所见的对端）。
	TrustedProxies []string `yaml:"trusted_proxies"`
	// MetricsToken /metrics 的 Bearer 令牌；为空时 /metrics 回退为登录会话认证。
	MetricsToken string `yaml:"metrics_token"`
	// AgentBinaryDir Agent 二进制目录，供 GET /api/agent/binary/{name} 下载（默认 ./dist）。
	AgentBinaryDir string `yaml:"agent_binary_dir"`
}

// DatabaseConfig PostgreSQL（必填）。
type DatabaseConfig struct {
	DSN             string `yaml:"dsn"`
	Host            string `yaml:"host"`
	Port            int    `yaml:"port"`
	User            string `yaml:"user"`
	Password        string `yaml:"password"`
	DBName          string `yaml:"dbname"`
	SSLMode         string `yaml:"sslmode"`
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime int    `yaml:"conn_max_lifetime"` // 秒
}

// AuthConfig 认证方式选择。
type AuthConfig struct {
	// Provider: builtin（内置 bcrypt 账号） / casdoor（FlyIAM SSO）
	Provider string `yaml:"provider"`
	// 内置管理员（provider=builtin 时使用）
	AdminUsername string `yaml:"admin_username"`
	AdminPassword string `yaml:"admin_password"` // 明文或 $2 开头 bcrypt 哈希
}

// ConsulConfig 可选：主机与业务分组数据源。
type ConsulConfig struct {
	Enabled        bool   `yaml:"enabled"`
	Address        string `yaml:"address"`
	Token          string `yaml:"token"`
	Datacenter     string `yaml:"datacenter"`
	Timeout        int    `yaml:"timeout"`     // 秒
	Concurrency    int    `yaml:"concurrency"` // 批量查询并发
	SyncOnStart    bool   `yaml:"sync_on_start"`
	SyncInterval   string `yaml:"sync_interval"`   // 如 5m/1h，空则不定时
	LeaderElection bool   `yaml:"leader_election"` // 启用 Leader 选举（多实例高可用）
}

// CasdoorConfig 可选：FlyIAM/Casdoor SSO。
type CasdoorConfig struct {
	Endpoint         string `yaml:"endpoint"`
	PublicEndpoint   string `yaml:"public_endpoint"`
	ClientId         string `yaml:"client_id"`
	ClientSecret     string `yaml:"client_secret"`
	Certificate      string `yaml:"certificate"`
	OrganizationName string `yaml:"organization"`
	ApplicationName  string `yaml:"application"`
	// RedirectURI OAuth2 回调地址，须与 Casdoor 应用中配置的一致。
	// 留空则由 server.external_url + /api/auth/callback 推导。
	RedirectURI string `yaml:"redirect_uri"`
}

// RedisConfig 可选：缓存。
type RedisConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	TTL      int    `yaml:"ttl"`
}

// ArtifactConfig 制品本地磁盘存储。
type ArtifactConfig struct {
	Dir          string `yaml:"dir"`          // 制品落盘目录（type=local）
	MaxSize      int64  `yaml:"max_size"`     // 单文件上限（字节），0=不限
	StorageType  string `yaml:"storage_type"` // local/s3/oss
	S3Endpoint   string `yaml:"s3_endpoint"`
	S3Region     string `yaml:"s3_region"`
	S3Bucket     string `yaml:"s3_bucket"`
	S3AccessKey  string `yaml:"s3_access_key"`
	S3SecretKey  string `yaml:"s3_secret_key"`
	S3UseSSL     bool   `yaml:"s3_use_ssl"`
	OSSEndpoint  string `yaml:"oss_endpoint"`
	OSSBucket    string `yaml:"oss_bucket"`
	OSSAccessKey string `yaml:"oss_access_key"`
	OSSSecretKey string `yaml:"oss_secret_key"`
}

// AgentConfig 与 Agent 通信/指令相关。
type AgentConfig struct {
	// PollHold 长轮询挂起时长（秒）；应显著小于任何前置代理的空闲超时。
	PollHold int `yaml:"poll_hold"`
	// LeaseMax 升级维护窗口全局上限（如 1h）。
	LeaseMax string `yaml:"lease_max"`
	// ClockSkew 指令时间戳允许偏移（如 5m）。
	ClockSkew string `yaml:"clock_skew"`
	// EnrollTokenTTL 一次性注册令牌有效期（如 15m）。
	EnrollTokenTTL string `yaml:"enroll_token_ttl"`
	// RequireEnrollToken 注册是否强制要求 enroll token（默认 true，内网可信可改 false）。
	RequireEnrollToken bool `yaml:"require_enroll_token"`
	// AllowExecMode 是否允许下发 exec/replace_exec 模式（默认 false，安全）。
	AllowExecMode bool `yaml:"allow_exec_mode"`
}

// LogConfig 日志。
type LogConfig struct {
	Level string `yaml:"level"`
}

// Default 返回内置默认配置（纯环境变量/无配置文件部署时的基线）。
func Default() *Config {
	return &Config{
		Server: ServerConfig{Host: "0.0.0.0", Port: 8090, WebEmbedded: true, AgentBinaryDir: "./dist"},
		Database: DatabaseConfig{
			Host: "localhost", Port: 5432, User: "postgres", DBName: "upgrade_mgr",
			SSLMode: "disable", MaxOpenConns: 50, MaxIdleConns: 10, ConnMaxLifetime: 3600,
		},
		Auth:     AuthConfig{Provider: "builtin", AdminUsername: "admin"},
		Consul:   ConsulConfig{Timeout: 10, Concurrency: 16},
		Redis:    RedisConfig{Enabled: false, Host: "localhost", Port: 6379, TTL: 300},
		Artifact: ArtifactConfig{Dir: "/var/lib/upgrade_mgr/artifacts"},
		Agent: AgentConfig{
			PollHold: 20, LeaseMax: "1h", ClockSkew: "5m",
			EnrollTokenTTL: "15m", RequireEnrollToken: true, AllowExecMode: false,
		},
		Log: LogConfig{Level: "info"},
	}
}

// Load 从 YAML 文件加载配置；path 为空或文件不存在时用默认值。随后应用环境变量覆盖。
func Load(path string) (*Config, error) {
	c := Default()
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if err := yaml.Unmarshal(data, c); err != nil {
				return nil, fmt.Errorf("解析配置文件失败: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("读取配置文件失败: %w", err)
		}
	}
	c.applyEnv()
	// 统一去掉结尾斜杠，避免拼接出 //api/... 导致 404。
	c.Server.ExternalURL = strings.TrimRight(c.Server.ExternalURL, "/")
	return c, nil
}

// applyEnv 用环境变量覆盖配置。
func (c *Config) applyEnv() {
	setStr(&c.Server.Host, "SERVER_HOST")
	setInt(&c.Server.Port, "SERVER_PORT")
	setStr(&c.Server.SessionSecret, "SESSION_SECRET")
	setStr(&c.Server.ExternalURL, "SERVER_EXTERNAL_URL")
	setBool(&c.Server.TrustProxy, "TRUST_PROXY")
	setStr(&c.Server.CookieSecure, "COOKIE_SECURE")
	if v := os.Getenv("CORS_ALLOWED_ORIGINS"); v != "" {
		c.Server.CORSOrigins = splitTrim(v)
	}
	if v := os.Getenv("TRUSTED_PROXIES"); v != "" {
		c.Server.TrustedProxies = splitTrim(v)
	}
	setStr(&c.Server.MetricsToken, "METRICS_TOKEN")
	setStr(&c.Server.AgentBinaryDir, "AGENT_BINARY_DIR")
	if c.Server.AgentBinaryDir == "" {
		c.Server.AgentBinaryDir = "./dist"
	}

	setStr(&c.Database.DSN, "DATABASE_URL")
	setStr(&c.Database.Host, "DB_HOST")
	setInt(&c.Database.Port, "DB_PORT")
	setStr(&c.Database.User, "DB_USER")
	setStr(&c.Database.Password, "DB_PASSWORD")
	setStr(&c.Database.DBName, "DB_NAME")
	setStr(&c.Database.SSLMode, "DB_SSLMODE")

	setStr(&c.Auth.Provider, "AUTH_PROVIDER")
	setStr(&c.Auth.AdminUsername, "ADMIN_USERNAME")
	setStr(&c.Auth.AdminPassword, "ADMIN_PASSWORD")

	setBool(&c.Consul.Enabled, "CONSUL_ENABLED")
	setStr(&c.Consul.Address, "CONSUL_ADDRESS")
	setStr(&c.Consul.Token, "CONSUL_TOKEN")
	setStr(&c.Consul.Datacenter, "CONSUL_DATACENTER")
	setBool(&c.Consul.LeaderElection, "CONSUL_LEADER_ELECTION")
	setBool(&c.Consul.SyncOnStart, "CONSUL_SYNC_ON_START")
	setStr(&c.Consul.SyncInterval, "CONSUL_SYNC_INTERVAL")

	setStr(&c.Casdoor.Endpoint, "CASDOOR_ENDPOINT")
	setStr(&c.Casdoor.PublicEndpoint, "CASDOOR_PUBLIC_ENDPOINT")
	setStr(&c.Casdoor.ClientId, "CASDOOR_CLIENT_ID")
	setStr(&c.Casdoor.ClientSecret, "CASDOOR_CLIENT_SECRET")
	setStr(&c.Casdoor.Certificate, "CASDOOR_CERTIFICATE")
	setStr(&c.Casdoor.OrganizationName, "CASDOOR_ORGANIZATION")
	setStr(&c.Casdoor.ApplicationName, "CASDOOR_APPLICATION")

	setBool(&c.Redis.Enabled, "REDIS_ENABLED")
	setStr(&c.Redis.Host, "REDIS_HOST")
	setInt(&c.Redis.Port, "REDIS_PORT")
	setStr(&c.Redis.Password, "REDIS_PASSWORD")

	setStr(&c.Artifact.Dir, "ARTIFACT_DIR")
	setStr(&c.Log.Level, "LOG_LEVEL")

	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8090
	}
}

// DSN 返回数据库连接串（优先 DATABASE_URL，否则分项拼装）。
func (c *Config) DSN() string {
	if c.Database.DSN != "" {
		return c.Database.DSN
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Database.Host, c.Database.Port, c.Database.User,
		c.Database.Password, c.Database.DBName, c.Database.SSLMode)
}

// ValidateRequired 校验必填项（不含可选依赖）。
func (c *Config) ValidateRequired() error {
	if c.DSN() == "" || (c.Database.DSN == "" && c.Database.Host == "") {
		return fmt.Errorf("数据库未配置：请设置 DATABASE_URL 或 DB_HOST 等")
	}
	switch c.Auth.Provider {
	case "builtin", "casdoor":
	default:
		return fmt.Errorf("未知认证方式 %q（应为 builtin 或 casdoor）", c.Auth.Provider)
	}
	return nil
}

func setStr(dst *string, env string) {
	if v := os.Getenv(env); v != "" {
		*dst = v
	}
}

func setInt(dst *int, env string) {
	if v := os.Getenv(env); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

func setBool(dst *bool, env string) {
	if v := os.Getenv(env); v != "" {
		*dst = v == "true" || v == "1"
	}
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
