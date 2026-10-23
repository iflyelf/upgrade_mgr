// Package svc 组装服务端运行时上下文。
//
// 启动原则：数据库为必填（失败即退出）；Consul/Casdoor/Redis 为可选依赖，
// 探测失败仅告警、不阻塞启动，功能相应降级。
package svc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/logic/artifact"
	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	synclogic "github.com/iflyelf/upgrade_mgr/internal/logic/sync"
	"github.com/iflyelf/upgrade_mgr/internal/logic/task"
	"github.com/iflyelf/upgrade_mgr/internal/maintenance"
	"github.com/iflyelf/upgrade_mgr/internal/metrics"
	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/consul"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbinprobe"
	"github.com/iflyelf/upgrade_mgr/internal/rbac"
	"github.com/iflyelf/upgrade_mgr/internal/setting"
	"github.com/iflyelf/upgrade_mgr/internal/storage"
)

// ServiceContext 服务端上下文。
type ServiceContext struct {
	// Version 服务端构建版本，同时作为下发给 Agent 的目标版本展示值。
	Version    string
	Config     *config.Config
	DB         *sql.DB
	Settings   *setting.Service
	Auth       auth.Provider
	Syncer     *synclogic.Syncer
	Component  *component.Repo
	Artifact   *artifact.Repo
	Task       *task.Service
	AgentGW    *agentgw.Gateway
	Metrics    *metrics.Collector
	Maintainer *maintenance.Maintainer
	RBAC       *rbac.Service
	Audit      *audit.Logger // 审计日志

	// consulRef 原子指针：页面修改 Consul 配置后可热重载；可能为 nil（未启用/不可用）。
	consulRef atomic.Pointer[consul.Client]
}

// New 创建上下文。数据库连接失败会 log.Fatal。
func New(c *config.Config) *ServiceContext {
	db := mustOpenDB(c)

	// 自动迁移数据库（创建/更新 schema）
	log.Println("🔄 检查数据库版本...")
	if err := migrate.AutoMigrate(db); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}

	// 加载页面设置（DB 优先 / env 兜底）
	settings := setting.NewService(db, c)
	if err := settings.Load(context.Background()); err != nil {
		log.Printf("⚠️ 加载应用设置失败（用默认/env）: %v", err)
	}

	// 未显式配置会话密钥时，生成一次并持久化到 DB，供多副本/重启共享。
	ensureSessionSecret(db, c)

	compRepo := component.NewRepo(db)
	artRepo := initArtifactRepo(db, &c.Artifact)
	gw := agentgw.New(db)
	taskSvc := task.NewService(db, compRepo, artRepo, func() bool { return c.Agent.AllowExecMode })
	taskSvc.SetReconciler(gw.ReconcileTasks)
	svc := &ServiceContext{
		Config:     c,
		DB:         db,
		Settings:   settings,
		Syncer:     synclogic.NewSyncer(db),
		Component:  compRepo,
		Artifact:   artRepo,
		Task:       taskSvc,
		AgentGW:    gw,
		Metrics:    metrics.NewCollector(db),
		Maintainer: maintenance.New(db),
		RBAC:       rbac.NewService(db),
		Audit:      audit.NewLogger(db), // 审计日志
	}

	// 认证提供方（按配置选择，可切换）
	svc.Auth = buildAuth(c, db)

	// 可选：Consul（不可用不阻塞启动）
	svc.ReloadConsul()

	return svc
}

// Consul 返回当前 Consul 客户端（可能为 nil）。
func (s *ServiceContext) Consul() *consul.Client { return s.consulRef.Load() }

// ReloadConsul 依当前配置重建 Consul 客户端；未启用或不可用时置 nil 并告警。
func (s *ServiceContext) ReloadConsul() {
	c := s.Settings.Snapshot().Consul
	if !c.Enabled || c.Address == "" {
		s.consulRef.Store(nil)
		log.Printf("ℹ️ Consul 同步未启用（可在设置页开启）")
		return
	}
	cli, err := consul.New(consul.Config{
		Address:    c.Address,
		Token:      c.Token,
		Datacenter: c.Datacenter,
		Timeout:    time.Duration(c.Timeout) * time.Second,
	})
	if err != nil {
		log.Printf("⚠️ Consul 客户端创建失败（降级为本地清单）: %v", err)
		s.consulRef.Store(nil)
		return
	}
	if err := cli.TestConnection(); err != nil {
		log.Printf("⚠️ Consul 连接测试失败（降级为本地清单）: %v", err)
		s.consulRef.Store(nil)
		s.AgentGW.SetConsul(nil) // WriteServiceMeta 内部处理 nil 接收者
		return
	}
	s.consulRef.Store(cli)
	s.AgentGW.SetConsul(cli) // 注入到 AgentGW 以便 Meta 回写
	log.Printf("✅ Consul 已连接: %s (dc=%s)", c.Address, cli.Datacenter())
}

// StartOrchestrator 启动灰度编排循环（按批次推进 running 任务）。
//
// 所有副本都运行同一循环，每轮用 PostgreSQL advisory lock 抢占执行权；
// 单机模式下同样走这条路径，不需要 Consul，也不需要额外 HA 配置。
func (s *ServiceContext) StartOrchestrator(ctx context.Context) {
	go task.NewOrchestrator(s.Task).Run(ctx)
	log.Println("✅ 灰度编排器已启动（PostgreSQL advisory lock，单机/多副本通用）")
}

// StartMetrics 启动指标采集循环。
func (s *ServiceContext) StartMetrics(ctx context.Context) {
	go s.Metrics.Start(ctx)
	log.Println("✅ Prometheus 指标采集已启动")
}

// StartExtbinProbe 启动外部文件可用性探测（默认每分钟）。
//
// provider 由调用方注入（避免 svc 反过来依赖 handler）。
// 探测结果写入 Prometheus 指标：up / probe_seconds / cached / cached_bytes。
func (s *ServiceContext) StartExtbinProbe(ctx context.Context, provider extbinprobe.Provider, interval time.Duration) {
	if provider == nil {
		return
	}
	prober := extbinprobe.New(provider, interval)
	go prober.Run(ctx)
	log.Printf("✅ 外部文件可用性探测已启动（间隔 %s）", intervalOrDefault(interval))
}

// intervalOrDefault 仅用于日志展示。
func intervalOrDefault(d time.Duration) time.Duration {
	if d <= 0 {
		return extbinprobe.DefaultInterval
	}
	return d
}

// StartMaintenance 启动数据库自动维护（每日凌晨 2 点）。
func (s *ServiceContext) StartMaintenance(ctx context.Context) {
	go s.Maintainer.Start(ctx)
	log.Println("✅ 数据库自动维护已启动（每日凌晨 2 点）")
}

// StartConsulSync 按配置启动定时同步（可选）。返回停止函数。
func (s *ServiceContext) StartConsulSync(ctx context.Context) {
	snap := s.Settings.Snapshot()
	if snap.Consul.SyncOnStart {
		go s.runSyncOnce()
	}
	interval, err := time.ParseDuration(snap.Consul.SyncInterval)
	if err != nil || interval <= 0 {
		return // 未配置定时
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.runSyncOnce()
			}
		}
	}()
}

func (s *ServiceContext) runSyncOnce() {
	cli := s.Consul()
	if cli == nil {
		return
	}
	res, err := s.Syncer.SyncFromConsul(context.Background(), cli, s.Config.Consul.CleanupMissing)
	if errors.Is(err, synclogic.ErrSyncInProgress) {
		return // 其他副本/请求正在同步，本轮跳过
	}
	if err != nil {
		log.Printf("⚠️ Consul 同步失败: %v", err)
		return
	}
	log.Printf("✅ Consul 同步完成: 服务=%d 实例=%d 主机=%d 清理=%d 耗时=%s",
		res.Services, res.Instances, res.Upserted, res.Deleted, res.Duration)
	if res.CleanupSkipped != "" && res.CleanupSkipped != "未开启自动清理" {
		log.Printf("⚠️ Consul 清理已跳过: %s", res.CleanupSkipped)
	}
}

func mustOpenDB(c *config.Config) *sql.DB {
	db, err := sql.Open("postgres", c.DSN())
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	maxOpen := c.Database.MaxOpenConns
	if maxOpen > 0 && maxOpen < 4 {
		// 编排器/维护/Consul 同步各自可能持有 1 个 advisory lock 连接，
		// 同时还需要查询连接；过小会导致连接池互相等待。
		maxOpen = 4
		log.Println("⚠️ database.max_open_conns 过小，已调整为 4（建议 10+）")
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(c.Database.MaxIdleConns)
	db.SetConnMaxLifetime(time.Duration(c.Database.ConnMaxLifetime) * time.Second)
	if err := db.Ping(); err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}
	log.Println("✅ 数据库连接成功")
	return db
}

const sessionSecretSettingKey = "server.session_secret"

// ensureSessionSecret 在未显式配置 SESSION_SECRET 时，生成一次并持久化到 DB。
//
// 这样单机重启不会让所有人掉线，多副本也能共享同一密钥；显式设置 16+ 字节
// 的 SESSION_SECRET 时仍以显式值为准。该 key 不在设置页注册表中，不会回显。
func ensureSessionSecret(db *sql.DB, c *config.Config) {
	if len(c.Server.SessionSecret) >= 16 {
		return
	}
	if c.Server.SessionSecret != "" {
		log.Println("⚠️ SESSION_SECRET 过短（<16 字节），改用数据库持久化密钥")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var secret string
	err := db.QueryRowContext(ctx,
		`SELECT value FROM app_settings WHERE key=$1`, sessionSecretSettingKey).Scan(&secret)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("⚠️ 读取数据库 SESSION_SECRET 失败: %v", err)
		return
	}
	if len(secret) >= 16 {
		c.Server.SessionSecret = secret
		log.Println("ℹ️ 已使用数据库持久化的 SESSION_SECRET（多副本/重启共享）")
		return
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		log.Printf("⚠️ 生成 SESSION_SECRET 失败: %v", err)
		return
	}
	generated := base64.RawURLEncoding.EncodeToString(buf)
	// 并发启动时只保留第一个有效值；若其他副本已写入 16+ 字节密钥则不覆盖。
	if _, err := db.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,NOW())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()
		WHERE length(app_settings.value) < 16`, sessionSecretSettingKey, generated); err != nil {
		log.Printf("⚠️ 持久化 SESSION_SECRET 失败: %v", err)
		return
	}
	if err := db.QueryRowContext(ctx,
		`SELECT value FROM app_settings WHERE key=$1`, sessionSecretSettingKey).Scan(&secret); err != nil {
		log.Printf("⚠️ 读取数据库 SESSION_SECRET 失败: %v", err)
		return
	} else if len(secret) < 16 {
		log.Println("⚠️ 数据库中的 SESSION_SECRET 长度不足 16 字节")
		return
	}
	c.Server.SessionSecret = secret
	log.Println("ℹ️ SESSION_SECRET 未显式配置，已生成并持久化到数据库（多副本/重启共享）")
}

func buildAuth(c *config.Config, db *sql.DB) auth.Provider {
	if len(c.Server.SessionSecret) < 16 {
		log.Println("⚠️ SESSION_SECRET 未设置或过短（<16 字节）：多副本会话/Casdoor 登录不互通，重启后需重新登录；生产请设置所有副本一致的 32+ 字节密钥")
	}
	if c.Auth.Provider == "casdoor" {
		redirect := c.Casdoor.RedirectURI
		if redirect == "" && c.Server.ExternalURL != "" {
			// 未显式配置时按对外地址推导，减少误配导致「无人能登录」。
			redirect = strings.TrimRight(c.Server.ExternalURL, "/") + "/api/auth/callback"
		}
		if redirect == "" {
			log.Println("⚠️ casdoor 未配置 redirect_uri 且无 external_url，OAuth 回调将失败")
		}
		log.Printf("ℹ️ 认证方式: casdoor (FlyIAM SSO)，回调=%s", redirect)
		return auth.NewCasdoor(auth.CasdoorOptions{
			Endpoint:       c.Casdoor.Endpoint,
			PublicEndpoint: c.Casdoor.PublicEndpoint,
			ClientID:       c.Casdoor.ClientId,
			ClientSecret:   c.Casdoor.ClientSecret,
			Organization:   c.Casdoor.OrganizationName,
			Application:    c.Casdoor.ApplicationName,
			RedirectURI:    redirect,
			SessionSecret:  c.Server.SessionSecret,
			CookieSecure:   cookieSecure(c),
		})
	}
	// 默认 builtin
	b := auth.NewBuiltin(db, c.Server.SessionSecret, cookieSecure(c))
	if err := b.EnsureAdmin(context.Background(), c.Auth.AdminUsername, c.Auth.AdminPassword); err != nil {
		log.Printf("⚠️ 初始化内置管理员失败: %v", err)
	}
	log.Printf("ℹ️ 认证方式: builtin（管理员: %s）", c.Auth.AdminUsername)
	return b
}

// cookieSecure 决定会话 Cookie 是否带 Secure 属性。
//
// auto（默认）：依 ExternalURL 是否为 https 推断，避免 HTTPS 部署下令牌明文泄露，
// 同时不影响纯内网 HTTP 调试。显式 true/false 可覆盖。
func cookieSecure(c *config.Config) bool {
	switch strings.ToLower(strings.TrimSpace(c.Server.CookieSecure)) {
	case "true":
		return true
	case "false":
		return false
	default: // auto / 未设置
		return strings.HasPrefix(strings.ToLower(c.Server.ExternalURL), "https://")
	}
}

// initArtifactRepo 按 StorageType 初始化制品仓储（local/s3/oss）。
func initArtifactRepo(db *sql.DB, cfg *config.ArtifactConfig) *artifact.Repo {
	stype := cfg.StorageType
	if stype == "" {
		stype = "local"
	}
	switch stype {
	case "local":
		return artifact.NewRepo(db, cfg.Dir)
	case "s3":
		scfg := storage.Config{
			Type: "s3",
			S3: storage.S3Config{
				Endpoint:        cfg.S3Endpoint,
				Region:          cfg.S3Region,
				Bucket:          cfg.S3Bucket,
				AccessKeyID:     cfg.S3AccessKey,
				SecretAccessKey: cfg.S3SecretKey,
				UseSSL:          cfg.S3UseSSL,
			},
		}
		backend, err := storage.NewBackend(scfg)
		if err != nil {
			log.Printf("❌ S3 后端初始化失败，降级为 local: %v", err)
			return artifact.NewRepo(db, cfg.Dir)
		}
		log.Printf("✅ 制品存储：S3 (%s/%s)", cfg.S3Endpoint, cfg.S3Bucket)
		return artifact.NewRepoWithBackend(db, cfg.Dir, backend, "s3")
	case "oss":
		ocfg := storage.Config{
			Type: "oss",
			OSS: storage.OSSConfig{
				Endpoint:        cfg.OSSEndpoint,
				Bucket:          cfg.OSSBucket,
				AccessKeyID:     cfg.OSSAccessKey,
				AccessKeySecret: cfg.OSSSecretKey,
			},
		}
		backend, err := storage.NewBackend(ocfg)
		if err != nil {
			log.Printf("❌ OSS 后端初始化失败，降级为 local: %v", err)
			return artifact.NewRepo(db, cfg.Dir)
		}
		log.Printf("✅ 制品存储：OSS (%s/%s)", cfg.OSSEndpoint, cfg.OSSBucket)
		return artifact.NewRepoWithBackend(db, cfg.Dir, backend, "oss")
	default:
		log.Printf("⚠️ 未知 storage_type=%q，使用 local", stype)
		return artifact.NewRepo(db, cfg.Dir)
	}
}
