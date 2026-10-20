// Package svc 组装服务端运行时上下文。
//
// 启动原则：数据库为必填（失败即退出）；Consul/Casdoor/Redis 为可选依赖，
// 探测失败仅告警、不阻塞启动，功能相应降级。
package svc

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/leader"
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
	"github.com/iflyelf/upgrade_mgr/internal/rbac"
	"github.com/iflyelf/upgrade_mgr/internal/setting"
	"github.com/iflyelf/upgrade_mgr/internal/storage"
)

// ServiceContext 服务端上下文。
type ServiceContext struct {
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

	compRepo := component.NewRepo(db)
	artRepo := initArtifactRepo(db, &c.Artifact)
	svc := &ServiceContext{
		Config:     c,
		DB:         db,
		Settings:   settings,
		Syncer:     synclogic.NewSyncer(db),
		Component:  compRepo,
		Artifact:   artRepo,
		Task:       task.NewService(db, compRepo, artRepo, c.Agent.AllowExecMode),
		AgentGW:    agentgw.New(db),
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
// 若启用 Consul，则使用 Leader 选举（仅 Leader 执行编排）。
func (s *ServiceContext) StartOrchestrator(ctx context.Context) {
	consul := s.Consul()
	if consul != nil && s.Config.Consul.LeaderElection {
		// 分布式模式：Leader 选举
		go s.startOrchestratorWithLeaderElection(ctx)
		log.Println("✅ 灰度编排器已启动（分布式模式，Leader 选举）")
		return
	}
	if s.Config.Consul.Enabled && s.Config.Consul.LeaderElection {
		// 配置了 HA 但 Consul 启动时不可用：保留单机模式以免任务停滞，
		// 但必须显式告警——多副本同时运行会重复下发升级指令。
		log.Println("⚠️ 已配置 Consul Leader 选举但 Consul 不可用，当前退化为单机编排；多副本可能重复下发，请检查 Consul")
	} else if s.Config.Consul.Enabled {
		log.Println("⚠️ Consul 已启用但未开启 leader_election，多副本会重复编排任务")
	}
	// 单机模式
	go task.NewOrchestrator(s.Task).Run(ctx)
	log.Println("✅ 灰度编排器已启动（单机模式）")
}

func (s *ServiceContext) startOrchestratorWithLeaderElection(ctx context.Context) {
	consul := s.Consul()
	if consul == nil {
		return
	}

	election := leader.NewElection(consul.API(), "service/upgrade-mgr/orchestrator-leader")
	orch := task.NewOrchestrator(s.Task)

	// 使用 Run 循环：自动处理 session 失效后重新竞选
	election.Run(ctx, func(leaderCtx context.Context) {
		log.Println("✅ 成为 Leader，启动编排器")
		orch.Run(leaderCtx)
		log.Println("⚠️ 失去 Leader，停止编排")
	})
}

// StartMetrics 启动指标采集循环。
func (s *ServiceContext) StartMetrics(ctx context.Context) {
	go s.Metrics.Start(ctx)
	log.Println("✅ Prometheus 指标采集已启动")
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
	res, err := s.Syncer.SyncFromConsul(context.Background(), cli)
	if err != nil {
		log.Printf("⚠️ Consul 同步失败: %v", err)
		return
	}
	log.Printf("✅ Consul 同步完成: 服务=%d 实例=%d 主机=%d 耗时=%s",
		res.Services, res.Instances, res.Upserted, res.Duration)
}

func mustOpenDB(c *config.Config) *sql.DB {
	db, err := sql.Open("postgres", c.DSN())
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	db.SetMaxOpenConns(c.Database.MaxOpenConns)
	db.SetMaxIdleConns(c.Database.MaxIdleConns)
	db.SetConnMaxLifetime(time.Duration(c.Database.ConnMaxLifetime) * time.Second)
	if err := db.Ping(); err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}
	log.Println("✅ 数据库连接成功")
	return db
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
