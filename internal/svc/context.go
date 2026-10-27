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
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/logic/artifact"
	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/logic/org"
	synclogic "github.com/iflyelf/upgrade_mgr/internal/logic/sync"
	"github.com/iflyelf/upgrade_mgr/internal/logic/task"
	"github.com/iflyelf/upgrade_mgr/internal/maintenance"
	"github.com/iflyelf/upgrade_mgr/internal/metrics"
	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/audit"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/cache"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/consul"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/consulmgr"
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
	// cacheRef 可选 Redis 缓存（单点/集群/哨兵）；未启用时所有读写自动降级。
	// 用原子指针保存，设置页保存后热替换不会与请求读取产生数据竞争。
	cacheRef atomic.Pointer[cache.Cache]

	// consulRef 原子指针：页面修改 Consul 配置后可热重载；可能为 nil（未启用/不可用）。
	consulRef atomic.Pointer[consul.Client]

	// dbMaxOpenBase / dbMaxIdleBase 是配置文件中的连接池基数。
	// 运行时会按 artifact.max_concurrent_uploads 自动抬高，但不得把
	// “自动抬高后的值”误当成新的基数（否则下调上传并发后无法回落）。
	dbMaxOpenBase int
	dbMaxIdleBase int
	// artifactReloadMu 串行化页面设置触发上传并发/锁等待热更新。
	artifactReloadMu sync.Mutex
}

// newMaintainer 构造数据库维护器，并注入可通过设置页热更新的审计保留天数。
//
// 必须通过 setting.Service 快照读取：设置页 Apply 会并发写回共享 Config，
// 直接读 c.Server.AuditRetentionDays 会与写入产生 data race。
func newMaintainer(c *config.Config, db *sql.DB, settings *setting.Service) *maintenance.Maintainer {
	m := maintenance.New(db)
	m.SetAuditRetention(func() int {
		if settings != nil {
			return settings.Snapshot().Server.AuditRetentionDays
		}
		if c != nil {
			return c.Server.AuditRetentionDays
		}
		return 0
	})
	return m
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
	// 设置加载完成后再按 artifact.max_concurrent_uploads 调整连接池：
	// 每个并发上传会持有一条 advisory lock 连接，必须预留足够容量的连接池。
	tuneDBPool(db, c.Database.MaxOpenConns, c.Database.MaxIdleConns, c.Artifact.MaxConcurrentUploads)

	// 未显式配置会话密钥时，生成一次并持久化到 DB，供多副本/重启共享。
	ensureSessionSecret(db, c)

	compRepo := component.NewRepo(db)
	artRepo := initArtifactRepo(db, &c.Artifact)
	artRepo.SetUploadConcurrency(c.Artifact.MaxConcurrentUploads)
	artRepo.SetLockWait(artifactLockWait(c.Artifact.LockWait))
	log.Printf("ℹ️ 制品上传配置: 并发上限=%d，跨副本锁等待=%s",
		c.Artifact.MaxConcurrentUploads, artifactLockWait(c.Artifact.LockWait))
	gw := agentgw.New(db)
	// 读设置时统一走 Snapshot：设置页 Apply 会并发写回 Config，
	// 直接读 *Config 字段会与写入产生 data race。
	taskSvc := task.NewService(db, compRepo, artRepo, func() bool {
		return settings.Snapshot().Agent.AllowExecMode
	})
	taskSvc.SetReconciler(gw.ReconcileTasks)
	taskSvc.SetOfflineAfter(func() string {
		return settings.Snapshot().Agent.OfflineAfter
	})
	// 组件级权限：无 deploy 权限的用户不能发任务（管理员不受限）。
	orgSvc := org.NewService(db)
	if fn := org.DeployAuthorizer(orgSvc); fn != nil {
		taskSvc.SetAuthorizer(fn)
	}
	if fn := org.HostGroupResolver(orgSvc); fn != nil {
		taskSvc.SetHostGroupResolver(fn)
	}
	// 任务仅创建者可操作（默认关闭，可在设置页开启）；
	// 团队级策略（teams.owner_only）优先于全局开关。
	// 设置页可热更新任务 owner 策略，必须通过快照读取，避免与 Apply 并发写 Config。
	taskSvc.SetOwnerOnlyPolicy(func() bool { return settings.Snapshot().Agent.TaskOwnerOnly })
	taskSvc.SetOwnerPolicyResolver(orgSvc.TeamOwnerPolicy)
	taskSvc.SetOwnerShareChecker(orgSvc.CanShareTasks)
	taskSvc.SetTeamMateChecker(orgSvc.SharesTeam)
	svc := &ServiceContext{
		Config:        c,
		DB:            db,
		Settings:      settings,
		Syncer:        synclogic.NewSyncer(db),
		Component:     compRepo,
		Artifact:      artRepo,
		Task:          taskSvc,
		AgentGW:       gw,
		Metrics:       metrics.NewCollector(db),
		Maintainer:    newMaintainer(c, db, settings),
		RBAC:          rbac.NewService(db),
		Audit:         audit.NewLogger(db), // 审计日志
		dbMaxOpenBase: c.Database.MaxOpenConns,
		dbMaxIdleBase: c.Database.MaxIdleConns,
	}
	// 可选：Redis（不可用仅降级，不阻塞启动）
	svc.cacheRef.Store(buildCache(&c.Redis))

	// 认证提供方（按配置选择，可切换）
	svc.Auth = buildAuth(c, db)

	// 可选：Consul（不可用不阻塞启动）
	svc.ReloadConsul()

	return svc
}

// buildCache 按配置创建 Redis 缓存；失败仅禁用缓存。
func buildCache(c *config.RedisConfig) *cache.Cache {
	ttl := time.Duration(c.TTL) * time.Second
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	return cache.New(cache.Options{
		Enabled: c.Enabled, Mode: c.Mode, Host: c.Host, Port: c.Port,
		Addrs: c.Hosts, MasterName: c.MasterName, Password: c.Password,
		DB: c.DB, TTL: ttl, HostsTTL: time.Duration(c.HostsTTL) * time.Second,
		Prefix: c.Prefix,
	})
}

// ReloadCache 依当前配置重建 Redis 连接（设置页保存后调用）。
func (s *ServiceContext) ReloadCache() {
	if s.Settings == nil {
		return
	}
	snap := s.Settings.Snapshot()
	if old := s.cacheRef.Swap(buildCache(&snap.Redis)); old != nil {
		old.Close()
	}
}

// GetCache 返回当前缓存实例（可能为 nil，调用方需判空）。
func (s *ServiceContext) GetCache() *cache.Cache {
	if s == nil {
		return nil
	}
	return s.cacheRef.Load()
}

// ConfigSnapshot 返回配置的并发安全快照。
//
// 设置页保存时会通过 setting.Apply 写回共享的 *config.Config；请求处理路径
// 必须使用本方法读取可热更新的配置，不能直接读 s.Config.xxx，否则会触发
// data race（go test -race 可复现）。
func (s *ServiceContext) ConfigSnapshot() config.Config {
	if s == nil {
		return config.Config{}
	}
	if s.Settings != nil {
		return s.Settings.Snapshot()
	}
	if s.Config != nil {
		return *s.Config
	}
	return config.Config{}
}

// ReloadArtifactSettings 用最新页面设置热更新上传并发、锁等待和连接池。
//
// 由设置保存 handler 在 artifact.max_concurrent_uploads / artifact.lock_wait
// 变更后调用；无需重启。上传限流器支持动态调整，锁等待为原子值，
// 连接池通过 database/sql 的 Set* 方法在线调整。
func (s *ServiceContext) ReloadArtifactSettings() {
	if s == nil || s.Settings == nil || s.Artifact == nil {
		return
	}
	s.artifactReloadMu.Lock()
	defer s.artifactReloadMu.Unlock()

	snap := s.Settings.Snapshot()
	uploads := snap.Artifact.MaxConcurrentUploads
	if uploads <= 0 {
		uploads = 4
	}
	if uploads > 64 {
		uploads = 64
	}
	wait := artifactLockWait(snap.Artifact.LockWait)
	s.Artifact.SetUploadConcurrency(uploads)
	s.Artifact.SetLockWait(wait)
	tuneDBPool(s.DB, s.dbMaxOpenBase, s.dbMaxIdleBase, uploads)
	log.Printf("🔄 制品上传配置已热更新: 并发上限=%d，跨副本锁等待=%s", uploads, wait)
}

// artifactRuntimeSettings 是可在运行期热更新、且需要跨副本同步设置的键。
var artifactRuntimeSettings = []string{
	"artifact.max_concurrent_uploads",
	"artifact.lock_wait",
}

// StartArtifactSettingsWatcher 周期性检查数据库中的制品上传运行时配置。
//
// 页面保存时当前实例由 handler 立即热更新；其他副本通过该轮询在 interval
// 内同步（默认 5 秒），无需重启。设置批量保存使用单事务，因此这里要么读到
// 整批新值，要么读到整批旧值。
func (s *ServiceContext) StartArtifactSettingsWatcher(ctx context.Context, interval time.Duration) {
	if s == nil || s.Settings == nil || s.Artifact == nil || s.DB == nil {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var last string
		syncOnce := func() {
			vals, err := s.Settings.ReadValues(ctx, artifactRuntimeSettings...)
			if err != nil {
				return // 数据库暂不可用：下一轮重试
			}
			cur := artifactSettingsFingerprint(vals)
			if cur == last {
				return
			}
			last = cur
			if applied := s.Settings.ApplyRuntime(vals); len(applied) > 0 {
				s.ReloadArtifactSettings()
				log.Printf("🔄 已同步其他副本的制品上传配置: %s", strings.Join(applied, ", "))
			}
		}
		syncOnce()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				syncOnce()
			}
		}
	}()
}

// artifactSettingsFingerprint 生成固定顺序的配置指纹，用于判断 DB 值是否变化。
func artifactSettingsFingerprint(vals map[string]string) string {
	var b strings.Builder
	for _, k := range artifactRuntimeSettings {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(vals[k])
		b.WriteByte('\x00')
	}
	return b.String()
}

// Consul 返回当前 Consul 客户端（可能为 nil）。
func (s *ServiceContext) Consul() *consul.Client { return s.consulRef.Load() }

// ConsulMgr 按当前设置构造 consul_mgr 客户端；未配置地址或令牌时返回 nil。
//
// 不做长连接缓存：同步是低频操作，每次按最新设置构造即可，
// 页面改完「设置 → consul_mgr 对接」立即生效，无需重启或显式重载。
func (s *ServiceContext) ConsulMgr() *consulmgr.Client {
	if s == nil || s.Settings == nil {
		return nil
	}
	c := s.Settings.Snapshot().ConsulMgr
	return consulmgr.NewClientTimeout(c.Endpoint, c.ServiceToken,
		time.Duration(c.Timeout)*time.Second)
}

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
	snap := s.Settings.Snapshot()
	res, err := s.Syncer.SyncFromConsul(context.Background(), cli,
		snap.Consul.CleanupMissing, snap.Consul.Concurrency)
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

// tuneDBPool 根据并发制品上传数调整数据库连接池。
//
// 每个上传会占用 1 条连接持有 PostgreSQL advisory lock，此外常规查询、
// 编排器/维护/同步也需要连接。连接池下限 = 上传并发数 + 4 条余量。
// baseOpen/baseIdle 是配置文件里的基数；运行期自动抬高不会改基数，
// 因此页面下调并发上传数时可以回落到合理值。配置值 0 表示不限制。
//
// 该函数只调用 database/sql 的线程安全 Set* 方法，不修改共享 Config，
// 可由设置页保存请求在运行期并发调用。
func tuneDBPool(db *sql.DB, baseOpen, baseIdle, uploads int) {
	if db == nil {
		return
	}
	if uploads <= 0 {
		uploads = 4
	}
	if uploads > 64 {
		uploads = 64
	}

	maxOpen := baseOpen
	if maxOpen > 0 && maxOpen < 4 {
		maxOpen = 4
	}
	if maxOpen > 0 {
		needed := uploads + 4
		if maxOpen < needed {
			log.Printf("⚠️ database.max_open_conns=%d 小于并发上传所需，运行期调整为 %d（上传 %d + 常规查询余量 4）",
				maxOpen, needed, uploads)
			maxOpen = needed
		}
	}
	db.SetMaxOpenConns(maxOpen)

	idle := baseIdle
	if idle < 0 {
		idle = 0
	}
	if maxOpen > 0 && idle > maxOpen {
		idle = maxOpen
	}
	db.SetMaxIdleConns(idle)
	if maxOpen == 0 {
		log.Printf("ℹ️ 数据库连接池: max_open=不限，max_idle=%d（并发上传上限 %d）", idle, uploads)
	} else {
		log.Printf("ℹ️ 数据库连接池: max_open=%d，max_idle=%d（并发上传上限 %d）", maxOpen, idle, uploads)
	}
}

// artifactLockWait 解析配置的跨副本制品锁等待时长；非法值回退默认 30m。
func artifactLockWait(raw string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return 30 * time.Minute
	}
	if d > 24*time.Hour {
		return 24 * time.Hour
	}
	return d
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
