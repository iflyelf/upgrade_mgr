// upgrade-mgr 控制台入口。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/handler"
	"github.com/iflyelf/upgrade_mgr/internal/middleware"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/netutil"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	configFile  = flag.String("c", "etc/config.yaml", "配置文件路径")
	showVersion = flag.Bool("version", false, "显示版本号并退出")
	version     = "0.1.0"
)

func main() {
	flag.Parse()
	if *showVersion {
		fmt.Printf("upgrade-mgr version %s\n", version)
		return
	}

	c, err := config.Load(*configFile)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if err := c.ValidateRequired(); err != nil {
		log.Fatalf("配置校验失败: %v", err)
	}

	sc := svc.New(c)
	sc.Version = version

	// 可选：Consul 定时同步（未启用则不启动）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc.StartConsulSync(ctx)
	sc.StartOrchestrator(ctx)
	sc.StartMetrics(ctx)
	sc.StartMaintenance(ctx)
	// 外部文件可用性探测（Prometheus 指标），便于观察公网/内网镜像稳定性。
	sc.StartExtbinProbe(ctx, handler.NewExtbinProvider(sc), 0)
	// 其他副本在页面保存的制品上传配置：当前实例由保存请求立即热更新，
	// 其他副本通过 5 秒轮询同步，无需重启。
	sc.StartArtifactSettingsWatcher(ctx, 5*time.Second)

	mux := http.NewServeMux()
	limiters := registerRoutes(mux, sc)

	// 访问日志 → CORS → 安全响应头 → CSRF → 请求体限制 → Redis 缓存 → 业务路由。
	// 缓存放在安全头内侧：命中缓存时仍会经过 SecurityHeaders / LimitBody，
	// 不会出现「命中 Redis 就缺少安全头」的问题。
	var root http.Handler = mux
	// 始终挂载：Redis 未启用/不可用时中间件原样透传，设置页启用后无需重启。
	root = middleware.Cache(sc.GetCache)(root)
	if c := sc.GetCache(); c != nil && c.Enabled() {
		log.Printf("✅ Redis HTTP 缓存已挂载（TTL %s，前缀 %s）", c.TTL(), c.Status().Prefix)
	}
	root = middleware.LimitBody(1<<20, "POST /api/artifacts")(root)
	// 写请求 CSRF 纵深防御：会话 Cookie 已是 SameSite=Lax，这里再校验
	// Origin/Referer 与请求 Host（或 CORS/ExternalURL 白名单）一致。
	// 允许的 Origin 每次请求读取设置快照，保存后立即生效。
	allowedOrigins := func() []string {
		snap := sc.Settings.Snapshot()
		out := append([]string(nil), snap.Server.CORSOrigins...)
		if v := strings.TrimRight(strings.TrimSpace(snap.Server.ExternalURL), "/"); v != "" {
			out = append(out, v)
		}
		return out
	}
	root = middleware.CSRFGuard(allowedOrigins)(root)
	// 安全头在 CSRF 外侧：即使请求被 CSRF 拒绝，也会带上 no-store 等安全头。
	root = middleware.SecurityHeaders(root)
	// CORS 必须在 Redis 缓存外侧：缓存 HIT 也要按当前 Origin 动态加响应头。
	// 设置页保存 server.cors_origins 后立即生效；预检在进入 ServeMux 前处理。
	root = middleware.CORS(allowedOrigins)(root)
	root = middleware.AccessLog(root)

	// 可选 HTTPS / 双向 TLS：未配置证书时保持原有 HTTP 行为。
	tlsCfg, err := c.Server.ServerTLSConfig()
	if err != nil {
		log.Fatalf("TLS 配置错误: %v", err)
	}

	addr := fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: root,
		// 不设置全局 WriteTimeout，避免打断 Agent 长轮询。
		// 读超时覆盖请求头与请求体：仅靠 ReadHeaderTimeout 时，攻击者可以
		// 用极慢的请求体长期占用连接（slowloris）。30 分钟足以覆盖 2GB
		// 制品在常见内网带宽下的上传；超大文件建议经反向代理上传。
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       30 * time.Minute,
		// 空闲 keep-alive 连接超时，避免慢连接长期占用文件描述符。
		IdleTimeout: 120 * time.Second,
		// 限制请求头大小，降低内存压力。
		MaxHeaderBytes: 1 << 20,
		TLSConfig:      tlsCfg,
	}

	go func() {
		if tlsCfg != nil {
			scheme := "https"
			mode := "单向 TLS"
			if c.Server.TLSClientAuth == "require" {
				mode = "双向 TLS（要求 Agent 客户端证书）"
			}
			log.Printf("🚀 upgrade-mgr %s 监听 %s://%s（%s）", version, scheme, addr, mode)
			// 证书已在 tlsCfg 中，无需再次传文件路径。
			if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Fatalf("服务启动失败: %v", err)
			}
			return
		}
		log.Printf("🚀 upgrade-mgr %s 监听 http://%s", version, addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务启动失败: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("正在关闭...")

	// 取消所有后台任务（编排器、长轮询等）
	cancel()

	// 停止限流器（清理 goroutine）
	for _, rl := range limiters {
		rl.Stop()
	}

	// 给足够时间让长轮询正常结束（默认 20s + 余量）
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
}

// registerRoutes 注册 HTTP 路由（Go 1.22+ ServeMux 方法+路径模式）。
func registerRoutes(mux *http.ServeMux, sc *svc.ServiceContext) []*middleware.RateLimiter {
	auth := sc.Auth

	// 限流器（敏感路由）
	// trustProxy 必须与实际部署一致：直连暴露时为 false（XFF 可伪造，信任即可绕过限流）；
	// 位于受信反向代理之后时置 true，否则所有客户端会被并成代理这一个 IP 限流。
	trustProxy := sc.Config.Server.TrustProxy
	var trusted *netutil.ProxyList
	if trustProxy && len(sc.Config.Server.TrustedProxies) > 0 {
		var err error
		trusted, err = netutil.ParseTrustedProxies(sc.Config.Server.TrustedProxies)
		if err != nil {
			log.Printf("⚠️ 受信代理列表解析失败: %v", err)
		}
	}
	registerLimiter := middleware.NewRateLimiterWithProxies(10, 1*time.Minute, trustProxy, trusted) // Agent 注册
	loginLimiter := middleware.NewRateLimiterWithProxies(20, 1*time.Minute, trustProxy, trusted)    // 登录
	enrollLimiter := middleware.NewRateLimiterWithProxies(5, 1*time.Minute, trustProxy, trusted)    // Enroll Token

	// 公开
	mux.HandleFunc("GET /health", handler.Health())
	mux.HandleFunc("GET /ready", handler.Ready(sc))
	mux.HandleFunc("GET /api/auth/config", handler.AuthConfig(sc))
	mux.Handle("POST /api/auth/login", middleware.RateLimitMiddleware(loginLimiter)(http.HandlerFunc(handler.Login(sc))))
	mux.HandleFunc("POST /api/auth/logout", handler.Logout(sc))
	// OAuth2 回调（仅 casdoor 生效；builtin 下返回 404）
	mux.Handle("GET /api/auth/callback", middleware.RateLimitMiddleware(loginLimiter)(http.HandlerFunc(handler.AuthCallback(sc))))

	// 需登录
	mux.HandleFunc("GET /api/auth/userinfo", middleware.RequireAuth(auth, handler.UserInfo(sc)))
	mux.Handle("PUT /api/auth/password", middleware.RateLimitMiddleware(loginLimiter)(middleware.RequireAuth(auth, handler.ChangePassword(sc))))
	mux.HandleFunc("GET /api/hosts", middleware.RequireAuth(auth, handler.ListHosts(sc)))
	mux.HandleFunc("GET /api/hosts/facets", middleware.RequireAuth(auth, handler.HostFacets(sc)))
	mux.HandleFunc("POST /api/hosts/sync", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.SyncHosts(sc))))
	// 从 consul_mgr 同步（覆盖多个服务组/多套 Consul）；与直连 Consul 同步并存。
	mux.HandleFunc("GET /api/consul-mgr/groups", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.ListConsulMgrGroups(sc))))
	mux.HandleFunc("POST /api/hosts/sync-consul-mgr", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.SyncHostsFromConsulMgr(sc))))
	mux.HandleFunc("POST /api/consul-mgr/test", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.TestConsulMgr(sc))))
	mux.HandleFunc("PUT /api/hosts/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.HostUpdate(sc))))
	mux.HandleFunc("DELETE /api/hosts/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.HostDelete(sc))))
	mux.HandleFunc("POST /api/hosts/{id}/tunnel-test", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.HostTunnelTest(sc))))
	// Agent 自升级：管理员触发 + 查看最近状态；自动升级由 agent.auto_update 设置控制。
	mux.HandleFunc("POST /api/agents/self-update", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.AgentSelfUpdate(sc))))
	mux.HandleFunc("GET /api/agents/self-update", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.AgentSelfUpdateStatus(sc))))

	// 需超级管理员（全局配置影响所有租户）
	mux.HandleFunc("GET /api/settings", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.ListSettings(sc))))
	mux.HandleFunc("PUT /api/settings", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.UpdateSettings(sc))))

	// Agent 网关：register 匿名（需 enroll token）；heartbeat/poll/ack/stage/report 需 Bearer 认证
	mux.Handle("POST /api/agent/register", middleware.RateLimitMiddleware(registerLimiter)(http.HandlerFunc(handler.AgentRegister(sc))))
	agentAuth := middleware.RequireAgentAuth(sc)
	mux.Handle("POST /api/agent/heartbeat", agentAuth(http.HandlerFunc(handler.AgentHeartbeat(sc))))
	mux.Handle("POST /api/agent/commands/poll", agentAuth(http.HandlerFunc(handler.AgentPoll(sc))))
	mux.Handle("POST /api/agent/commands/{id}/ack", agentAuth(http.HandlerFunc(handler.AgentCommandAck(sc))))
	mux.Handle("POST /api/agent/commands/{id}/stage", agentAuth(http.HandlerFunc(handler.AgentCommandStage(sc))))
	mux.Handle("POST /api/agent/commands/{id}/report", agentAuth(http.HandlerFunc(handler.AgentCommandReport(sc))))
	mux.Handle("GET /api/agent/enroll-token", middleware.RateLimitMiddleware(enrollLimiter)(middleware.RequireAuth(auth, middleware.RequireAdmin(handler.AgentEnrollToken(sc)))))
	// 兼容旧客户端保留 GET；新页面使用 POST（GET 不应产生副作用，也避免顶级导航 CSRF）。
	mux.Handle("POST /api/agent/enroll-token", middleware.RateLimitMiddleware(enrollLimiter)(middleware.RequireAuth(auth, middleware.RequireAdmin(handler.AgentEnrollToken(sc)))))
	// 自动化注册令牌：永久/自定义到期、可限制次数/CIDR、可注销/轮换。
	mux.Handle("POST /api/agent/enroll-tokens", middleware.RateLimitMiddleware(enrollLimiter)(middleware.RequireAuth(auth, middleware.RequireAdmin(handler.AutomationEnrollTokenCreate(sc)))))
	mux.Handle("GET /api/agent/enroll-tokens", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.AutomationEnrollTokenList(sc))))
	mux.Handle("POST /api/agent/enroll-tokens/{id}/revoke", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.AutomationEnrollTokenRevoke(sc))))
	mux.Handle("POST /api/agent/enroll-tokens/{id}/rotate", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.AutomationEnrollTokenRotate(sc))))
	// Agent 二进制下载（无鉴权，白名单架构；支持 kernel 参数自动选择构建）
	mux.HandleFunc("GET /api/agent/binary/{name}", handler.AgentBinaryDownload(sc))
	// 白名单外部二进制（控制台代为下载缓存）：供无法 TLS 1.2 的老系统用 HTTP 拉取
	mux.HandleFunc("GET /api/external/binary/{name}", handler.ExternalBinaryDownload(sc))
	// 白名单管理（页面表格页）：查看 / 保存（热加载）/ 测试下载
	mux.HandleFunc("GET /api/external/binaries",
		middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.ListExternalBinaries(sc))))
	mux.HandleFunc("PUT /api/external/binaries",
		middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.SaveExternalBinaries(sc))))
	mux.HandleFunc("POST /api/external/binaries/test",
		middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.TestExternalBinary(sc))))
	mux.HandleFunc("POST /api/external/binaries/refresh",
		middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.RefreshExternalBinary(sc))))
	mux.HandleFunc("POST /api/external/binaries/refresh-all",
		middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.RefreshAllExternalBinaries(sc))))
	// 离线文件上传（平台无法出网时使用；优先于上游缓存）
	mux.HandleFunc("POST /api/external/binaries/{name}/upload",
		middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.UploadExternalBinary(sc))))
	mux.HandleFunc("DELETE /api/external/binaries/{name}/upload",
		middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.DeleteExternalBinaryUpload(sc))))

	// 组件管理
	mux.HandleFunc("GET /api/components", middleware.RequireAuth(auth, handler.ComponentList(sc)))
	mux.HandleFunc("GET /api/components/{id}", middleware.RequireAuth(auth, handler.ComponentGet(sc)))
	mux.HandleFunc("POST /api/components", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ComponentCreate(sc))))
	mux.HandleFunc("PUT /api/components/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ComponentUpdate(sc))))
	mux.HandleFunc("DELETE /api/components/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ComponentDelete(sc))))
	mux.HandleFunc("GET /api/components/{id}/references", middleware.RequireAuth(auth, handler.ComponentReferences(sc)))

	// 制品管理
	mux.HandleFunc("GET /api/artifacts", middleware.RequireAuth(auth, handler.ArtifactList(sc)))
	mux.HandleFunc("GET /api/artifacts/paged", middleware.RequireAuth(auth, handler.ArtifactListPaged(sc)))
	mux.HandleFunc("POST /api/artifacts", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ArtifactUpload(sc))))
	// 制品下载：Agent 用 Bearer token 或匿名内网访问（P2 放开，P3 收敛为签名 URL）
	// 制品下载：Agent 专用（Bearer 认证 + 租户作用域），防匿名拖取二进制
	mux.Handle("GET /api/artifacts/{id}/download", agentAuth(http.HandlerFunc(handler.ArtifactDownload(sc))))

	// 升级任务：登录即可访问；组件 deploy 权限、主机分组范围与 owner 策略由服务层校验。
	mux.HandleFunc("GET /api/tasks", middleware.RequireAuth(auth, handler.TaskList(sc)))
	mux.HandleFunc("GET /api/tasks/creators", middleware.RequireAuth(auth, handler.TaskCreators(sc)))
	mux.HandleFunc("GET /api/tasks/{id}", middleware.RequireAuth(auth, handler.TaskDetail(sc)))
	mux.HandleFunc("POST /api/tasks/preview", middleware.RequireAuth(auth, handler.TaskPreview(sc)))
	mux.HandleFunc("POST /api/tasks", middleware.RequireAuth(auth, handler.TaskCreate(sc)))
	mux.HandleFunc("POST /api/tasks/{id}/start", middleware.RequireAuth(auth, handler.TaskStart(sc)))
	mux.HandleFunc("POST /api/tasks/{id}/pause", middleware.RequireAuth(auth, handler.TaskPause(sc)))
	mux.HandleFunc("POST /api/tasks/{id}/promote", middleware.RequireAuth(auth, handler.TaskPromote(sc)))
	mux.HandleFunc("POST /api/tasks/{id}/cancel", middleware.RequireAuth(auth, handler.TaskCancel(sc)))
	mux.HandleFunc("POST /api/tasks/{id}/retry", middleware.RequireAuth(auth, handler.TaskRetry(sc)))
	mux.HandleFunc("DELETE /api/tasks/{id}", middleware.RequireAuth(auth, handler.TaskDelete(sc)))
	mux.HandleFunc("POST /api/tasks/batch-delete", middleware.RequireAuth(auth, handler.TaskBatchDelete(sc)))

	// 主机
	mux.HandleFunc("GET /api/agents/stats", middleware.RequireAuth(auth, handler.AgentStats(sc)))
	mux.HandleFunc("GET /api/hosts/{ip}/upgrade-history", middleware.RequireAuth(auth, handler.HostUpgradeHistory(sc)))

	// 用户与租户管理（管理员）：多租户可用性的必要入口
	mux.HandleFunc("GET /api/audit-logs", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.AuditLogList(sc))))
	mux.HandleFunc("GET /api/audit-logs/export", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.AuditLogExport(sc))))
	mux.HandleFunc("GET /api/quickstart", middleware.RequireAuth(auth, handler.Quickstart()))
	mux.HandleFunc("GET /api/my/permissions", middleware.RequireAuth(auth, handler.MyPermissions(sc)))
	// Redis 缓存：状态/自检/清理（管理员）
	mux.HandleFunc("GET /api/cache/status", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.CacheStatus(sc))))
	mux.HandleFunc("POST /api/cache/test", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.CacheTest(sc))))
	mux.HandleFunc("POST /api/cache/flush", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.CacheFlush(sc))))
	mux.HandleFunc("GET /api/users", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.UserList(sc))))
	mux.HandleFunc("POST /api/users", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.UserCreate(sc))))
	mux.HandleFunc("PUT /api/users/{username}/tenant-roles", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.UserSetTenantRoles(sc))))
	mux.HandleFunc("PUT /api/users/{username}/disabled", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.UserSetDisabled(sc))))
	mux.HandleFunc("DELETE /api/users/{username}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.UserDelete(sc))))
	mux.HandleFunc("POST /api/users/{username}/reset-password", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.UserResetPassword(sc))))
	// 字段自定义（用户/主机扩展字段定义）
	mux.HandleFunc("GET /api/field-defs/{kind}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ListFieldDefs(sc))))
	mux.HandleFunc("POST /api/field-defs/{kind}", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.CreateFieldDef(sc))))
	mux.HandleFunc("PUT /api/field-defs/{kind}/{id}", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.UpdateFieldDef(sc))))
	mux.HandleFunc("DELETE /api/field-defs/{kind}/{id}", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.DeleteFieldDef(sc))))
	// 主机字段映射（外部来源键 → 内置列/自定义字段；零硬编码）
	mux.HandleFunc("GET /api/host-field-mappings", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ListHostFieldMappings(sc))))
	mux.HandleFunc("POST /api/host-field-mappings", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.CreateHostFieldMapping(sc))))
	mux.HandleFunc("PUT /api/host-field-mappings/{id}", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.UpdateHostFieldMapping(sc))))
	mux.HandleFunc("DELETE /api/host-field-mappings/{id}", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.DeleteHostFieldMapping(sc))))
	// 扫描外部来源实际存在的键 + 勾选导入为字段定义与映射
	mux.HandleFunc("GET /api/host-field-mappings/scan", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ScanHostFieldKeys(sc))))
	mux.HandleFunc("POST /api/host-field-mappings/import", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.ImportHostFieldKeys(sc))))

	// 人员信息（用户扩展字段值）
	mux.HandleFunc("GET /api/users/fields", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ListUserFieldsWithValues(sc))))
	mux.HandleFunc("GET /api/users/{username}/fields", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.GetUserFields(sc))))
	mux.HandleFunc("PUT /api/users/{username}/fields", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.SaveUserFields(sc))))

	// FlyIAM 集成：连通性测试 + 用户字段定义同步
	mux.HandleFunc("POST /api/flyiam/test", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TestFlyIAM(sc))))
	mux.HandleFunc("POST /api/flyiam/sync-user-fields", middleware.RequireAuth(auth, middleware.RequireSuperAdmin(handler.SyncUserFieldsFromFlyIAM(sc))))
	mux.HandleFunc("POST /api/flyiam/sync-departments", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.SyncDepartmentsFromFlyIAM(sc))))
	mux.HandleFunc("GET /api/flyiam/users", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.FlyIAMUsers(sc))))
	mux.HandleFunc("POST /api/flyiam/users/{username}/provision", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.FlyIAMProvisionLocalUser(sc))))

	// 人员组织：角色 / 团队 / 服务组 / 授权
	mux.HandleFunc("GET /api/roles", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.RoleList(sc))))
	mux.HandleFunc("POST /api/roles", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.RoleCreate(sc))))
	mux.HandleFunc("PUT /api/roles/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.RoleUpdate(sc))))
	mux.HandleFunc("DELETE /api/roles/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.RoleDelete(sc))))
	mux.HandleFunc("GET /api/teams", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TeamList(sc))))
	mux.HandleFunc("POST /api/teams", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TeamCreate(sc))))
	mux.HandleFunc("PUT /api/teams/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TeamUpdate(sc))))
	mux.HandleFunc("DELETE /api/teams/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TeamDelete(sc))))
	mux.HandleFunc("GET /api/teams/{id}/members", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TeamMembers(sc))))
	mux.HandleFunc("POST /api/teams/{id}/members", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TeamMemberAdd(sc))))
	mux.HandleFunc("DELETE /api/teams/{id}/members/{user}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TeamMemberRemove(sc))))
	mux.HandleFunc("GET /api/teams/{id}/permissions", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TeamPermissions(sc))))
	mux.HandleFunc("PUT /api/teams/{id}/permissions", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TeamPermissionSet(sc))))
	mux.HandleFunc("DELETE /api/teams/{id}/permissions/{group}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TeamPermissionDelete(sc))))
	mux.HandleFunc("GET /api/host-groups", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.HostGroupOptions(sc))))
	mux.HandleFunc("GET /api/org/departments", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.DepartmentList(sc))))
	mux.HandleFunc("GET /api/service-groups", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.GroupList(sc))))
	mux.HandleFunc("POST /api/service-groups", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.GroupCreate(sc))))
	mux.HandleFunc("PUT /api/service-groups/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.GroupUpdate(sc))))
	mux.HandleFunc("DELETE /api/service-groups/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.GroupDelete(sc))))

	mux.HandleFunc("GET /api/tenants", middleware.RequireAuth(auth, handler.TenantList(sc)))
	mux.HandleFunc("POST /api/tenants", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TenantCreate(sc))))

	// Prometheus 指标（metrics_token 或会话认证）
	mux.Handle("GET /metrics", middleware.MetricsAuth(sc.Config.Server.MetricsToken, auth, func(w http.ResponseWriter, r *http.Request) {
		promhttp.Handler().ServeHTTP(w, r)
	}))

	// 前端（内嵌）：兜底
	if sc.Config.Server.WebEmbedded {
		mux.Handle("/", spaHandler())
	}
	return []*middleware.RateLimiter{registerLimiter, loginLimiter, enrollLimiter}
}
