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
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
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
	FlyIAM   FlyIAMConfig   `yaml:"flyiam"`
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
	// ExternalBinaries 允许控制台代为下载并缓存的外部文件白名单。
	//
	// 键是 /api/external/binary/{name} 中的 name，值是上游完整 URL；
	// 前缀 "script:" 表示按文本脚本处理（不做 MZ 校验），否则按可执行文件处理。
	// 典型用途：老系统（Windows Server 2008 R2 + PowerShell 2.0）不支持 TLS 1.2，
	// 无法直连 GitHub，由支持 TLS 1.2 的控制台代下载后通过 HTTP 提供给主机。
	//
	// 例：
	//   external_binaries:
	//     windows-exporter-amd64: https://.../windows_exporter-amd64.exe
	//     windows-exporter-install.ps1: script:https://.../install.ps1
	ExternalBinaries map[string]string `yaml:"external_binaries"`
	// ExternalBinaryHosts 允许代下载的上游域名白名单（逗号分隔，可含端口）。
	// 留空时使用内置默认（github.com / raw.githubusercontent.com / proxy.xiaonuo.live），
	// 防止配置被改成内网地址形成 SSRF。
	ExternalBinaryHosts []string `yaml:"external_binary_hosts"`
	// ExternalBinaryTTL 缓存有效期（如 24h）；<=0 使用默认 24h。
	ExternalBinaryTTL string `yaml:"external_binary_ttl"`
	// ExternalBinaryAllowPrivate 是否允许代下载内网/环回地址。
	//
	// 默认 false（拒绝内网，防 SSRF）。内网镜像站（如 http://nexus.corp:8081）
	// 需要放开时，建议只配置 ExternalBinaryAllowCIDRs 精确放行网段；
	// 只有确实无法枚举网段时才打开本开关。
	ExternalBinaryAllowPrivate bool `yaml:"external_binary_allow_private"`
	// ExternalBinaryAllowCIDRs 允许访问的内网网段/IP（逗号分隔，如 10.0.0.0/8,192.168.1.10）。
	ExternalBinaryAllowCIDRs []string `yaml:"external_binary_allow_cidrs"`
	// ---- 可选 HTTPS / 双向 TLS ----
	//
	// 留空 TLSCert/TLSKey 时以 HTTP 提供服务（与历史行为一致）。
	// 配置后控制台直接以 HTTPS 监听；若要求 Agent 持客户端证书，再配置
	// TLSClientCA + TLSClientAuth=require，即构成双向 TLS（mTLS）。
	TLSCert string `yaml:"tls_cert"` // 服务端证书 PEM
	TLSKey  string `yaml:"tls_key"`  // 服务端私钥 PEM
	// TLSClientCA 客户端 CA（PEM）；启用双向 TLS 时用于校验 Agent 客户端证书。
	TLSClientCA string `yaml:"tls_client_ca"`
	// TLSClientAuth 客户端证书策略：none（默认）/ request（可选校验）/ require（强制）。
	TLSClientAuth string `yaml:"tls_client_auth"`
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
	Enabled      bool   `yaml:"enabled"`
	Address      string `yaml:"address"`
	Token        string `yaml:"token"`
	Datacenter   string `yaml:"datacenter"`
	Timeout      int    `yaml:"timeout"`     // 秒
	Concurrency  int    `yaml:"concurrency"` // 批量查询并发
	SyncOnStart  bool   `yaml:"sync_on_start"`
	SyncInterval string `yaml:"sync_interval"` // 如 5m/1h，空则不定时
	// CleanupMissing 同步后清理已从 Consul 移除的纯 Consul 主机。
	// 只删除 source=consul、未装 Agent、无未完成任务/指令的主机。
	CleanupMissing bool `yaml:"cleanup_missing"`
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
	// AllowExecMode 是否允许下发 exec/replace_exec/script 模式以及执行命令任务。
	// 默认开启：平台核心场景就需要执行安装/卸载脚本；如仅做二进制替换，
	// 可在设置页或配置文件中关闭以收紧权限。
	AllowExecMode bool `yaml:"allow_exec_mode"`
	// AutoUpdate 是否在 Agent 心跳发现旧版本时自动下发自升级（默认 false）。
	// 开启后 Agent 会下载新二进制并重启，建议确认 dist 目录已放置目标版本再开启。
	AutoUpdate bool `yaml:"auto_update"`
}

// FlyIAMConfig FlyIAM 服务间集成（与 consul_mgr 复用同一部署）。
type FlyIAMConfig struct {
	// Endpoint FlyIAM 后端地址（服务端可达），如 http://flyiam.internal:8000
	Endpoint string `yaml:"endpoint"`
	// ServiceToken 服务间令牌，通过 X-Service-Token 发送。
	ServiceToken string `yaml:"service_token"`
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
		Consul:   ConsulConfig{Timeout: 10, Concurrency: 16, CleanupMissing: true},
		Redis:    RedisConfig{Enabled: false, Host: "localhost", Port: 6379, TTL: 300},
		Artifact: ArtifactConfig{Dir: "/var/lib/upgrade_mgr/artifacts"},
		Agent: AgentConfig{
			PollHold: 20, LeaseMax: "1h", ClockSkew: "5m",
			EnrollTokenTTL: "15m", RequireEnrollToken: true, AllowExecMode: true,
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
	// 启动时校验白名单：配置写错（非法名称/地址、非白名单域名）直接报错，
	// 避免运行期才发现下载全部失败。
	if len(c.Server.ExternalBinaries) > 0 {
		if err := extbin.ValidateConfig(c.Server.ExternalBinaries, c.Server.ExternalBinaryHosts); err != nil {
			return nil, fmt.Errorf("external_binaries 配置错误: %w", err)
		}
	}
	if err := extbin.ValidateCIDRs(c.Server.ExternalBinaryAllowCIDRs); err != nil {
		return nil, fmt.Errorf("external_binary_allow_cidrs 配置错误: %w", err)
	}
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
	// 白名单也可用环境变量提供，格式 name=url,name2=script:url2。
	if v := os.Getenv("EXTERNAL_BINARIES"); v != "" {
		if m, err := ParseExternalBinaries(v); err == nil {
			if c.Server.ExternalBinaries == nil {
				c.Server.ExternalBinaries = map[string]string{}
			}
			for k, u := range m {
				c.Server.ExternalBinaries[k] = u
			}
		}
	}
	if v := os.Getenv("EXTERNAL_BINARY_HOSTS"); v != "" {
		c.Server.ExternalBinaryHosts = splitTrim(v)
	}
	setStr(&c.Server.ExternalBinaryTTL, "EXTERNAL_BINARY_TTL")
	setBool(&c.Server.ExternalBinaryAllowPrivate, "EXTERNAL_BINARY_ALLOW_PRIVATE")
	if v := os.Getenv("EXTERNAL_BINARY_ALLOW_CIDRS"); v != "" {
		c.Server.ExternalBinaryAllowCIDRs = splitTrim(v)
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
	setBool(&c.Consul.SyncOnStart, "CONSUL_SYNC_ON_START")
	setStr(&c.Consul.SyncInterval, "CONSUL_SYNC_INTERVAL")
	setBool(&c.Consul.CleanupMissing, "CONSUL_CLEANUP_MISSING")

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
	setStr(&c.Artifact.StorageType, "ARTIFACT_STORAGE_TYPE")
	setStr(&c.Artifact.S3Endpoint, "ARTIFACT_S3_ENDPOINT")
	setStr(&c.Artifact.S3Region, "ARTIFACT_S3_REGION")
	setStr(&c.Artifact.S3Bucket, "ARTIFACT_S3_BUCKET")
	setStr(&c.Artifact.S3AccessKey, "ARTIFACT_S3_ACCESS_KEY")
	setStr(&c.Artifact.S3SecretKey, "ARTIFACT_S3_SECRET_KEY")
	setBool(&c.Artifact.S3UseSSL, "ARTIFACT_S3_USE_SSL")
	setStr(&c.Artifact.OSSEndpoint, "ARTIFACT_OSS_ENDPOINT")
	setStr(&c.Artifact.OSSBucket, "ARTIFACT_OSS_BUCKET")
	setStr(&c.Artifact.OSSAccessKey, "ARTIFACT_OSS_ACCESS_KEY")
	setStr(&c.Artifact.OSSSecretKey, "ARTIFACT_OSS_SECRET_KEY")
	setBool(&c.Agent.AutoUpdate, "AGENT_AUTO_UPDATE")
	setStr(&c.FlyIAM.Endpoint, "FLYIAM_ENDPOINT")
	setStr(&c.FlyIAM.ServiceToken, "FLYIAM_SERVICE_TOKEN")
	setStr(&c.Log.Level, "LOG_LEVEL")

	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8090
	}
	setStr(&c.Server.TLSCert, "SERVER_TLS_CERT")
	setStr(&c.Server.TLSKey, "SERVER_TLS_KEY")
	setStr(&c.Server.TLSClientCA, "SERVER_TLS_CLIENT_CA")
	setStr(&c.Server.TLSClientAuth, "SERVER_TLS_CLIENT_AUTH")
	if c.Server.TLSClientAuth == "" {
		c.Server.TLSClientAuth = "none"
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
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		return fmt.Errorf("tls_cert 与 tls_key 必须同时配置")
	}
	switch c.Server.TLSClientAuth {
	case "", "none":
	case "request", "require":
		if c.Server.TLSClientCA == "" {
			return fmt.Errorf("tls_client_auth=%s 时需配置 tls_client_ca", c.Server.TLSClientAuth)
		}
	default:
		return fmt.Errorf("tls_client_auth 只能是 none/request/require")
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

// ParseExternalBinaries 解析 "name=url,name2=script:url2" 形式的白名单。
//
// 用于环境变量 EXTERNAL_BINARIES；name 只允许字母数字、点、下划线、短横线。
func ParseExternalBinaries(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("白名单项 %q 缺少 =", pair)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !validExternalName(k) {
			return nil, fmt.Errorf("白名单名称 %q 非法（仅允许字母数字、点、下划线、短横线）", k)
		}
		if v == "" {
			return nil, fmt.Errorf("白名单 %s 缺少地址", k)
		}
		out[k] = v
	}
	return out, nil
}

// validExternalName 校验白名单名称，避免路径分隔符与控制字符。
func validExternalName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	return regexpExternalName.MatchString(s)
}

var regexpExternalName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

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
