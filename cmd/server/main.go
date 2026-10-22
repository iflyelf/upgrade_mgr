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

	mux := http.NewServeMux()
	limiters := registerRoutes(mux, sc)

	// 安全响应头（全局）→ 请求体限制（JSON 默认 1MB，制品上传除外）→ 访问日志
	var root http.Handler = middleware.SecurityHeaders(mux)
	root = middleware.LimitBody(1<<20, "POST /api/artifacts")(root)
	root = middleware.AccessLog(root)

	addr := fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: root,
		// 不设置全局 WriteTimeout，避免打断 Agent 长轮询；读超时保留。
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
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
	mux.HandleFunc("POST /api/hosts/sync", middleware.RequireAuth(auth, handler.SyncHosts(sc)))
	mux.HandleFunc("PUT /api/hosts/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.HostUpdate(sc))))
	mux.HandleFunc("DELETE /api/hosts/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.HostDelete(sc))))
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
	// Agent 二进制下载（无鉴权，白名单架构）
	mux.HandleFunc("GET /api/agent/binary/{name}", handler.AgentBinaryDownload(sc))

	// 组件管理
	mux.HandleFunc("GET /api/components", middleware.RequireAuth(auth, handler.ComponentList(sc)))
	mux.HandleFunc("GET /api/components/{id}", middleware.RequireAuth(auth, handler.ComponentGet(sc)))
	mux.HandleFunc("POST /api/components", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ComponentCreate(sc))))
	mux.HandleFunc("PUT /api/components/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ComponentUpdate(sc))))
	mux.HandleFunc("DELETE /api/components/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ComponentDelete(sc))))

	// 制品管理
	mux.HandleFunc("GET /api/artifacts", middleware.RequireAuth(auth, handler.ArtifactList(sc)))
	mux.HandleFunc("POST /api/artifacts", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.ArtifactUpload(sc))))
	// 制品下载：Agent 用 Bearer token 或匿名内网访问（P2 放开，P3 收敛为签名 URL）
	// 制品下载：Agent 专用（Bearer 认证 + 租户作用域），防匿名拖取二进制
	mux.Handle("GET /api/artifacts/{id}/download", agentAuth(http.HandlerFunc(handler.ArtifactDownload(sc))))

	// 升级任务（管理员权限）
	mux.HandleFunc("GET /api/tasks", middleware.RequireAuth(auth, handler.TaskList(sc)))
	mux.HandleFunc("GET /api/tasks/{id}", middleware.RequireAuth(auth, handler.TaskDetail(sc)))
	mux.HandleFunc("POST /api/tasks/preview", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TaskPreview(sc))))
	mux.HandleFunc("POST /api/tasks", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TaskCreate(sc))))
	mux.HandleFunc("POST /api/tasks/{id}/start", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TaskStart(sc))))
	mux.HandleFunc("POST /api/tasks/{id}/pause", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TaskPause(sc))))
	mux.HandleFunc("POST /api/tasks/{id}/promote", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TaskPromote(sc))))
	mux.HandleFunc("POST /api/tasks/{id}/cancel", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TaskCancel(sc))))
	mux.HandleFunc("DELETE /api/tasks/{id}", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.TaskDelete(sc))))

	// 主机
	mux.HandleFunc("GET /api/hosts/{ip}/upgrade-history", middleware.RequireAuth(auth, handler.HostUpgradeHistory(sc)))

	// 用户与租户管理（管理员）：多租户可用性的必要入口
	mux.HandleFunc("GET /api/users", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.UserList(sc))))
	mux.HandleFunc("POST /api/users", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.UserCreate(sc))))
	mux.HandleFunc("PUT /api/users/{username}/tenant-roles", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.UserSetTenantRoles(sc))))
	mux.HandleFunc("PUT /api/users/{username}/disabled", middleware.RequireAuth(auth, middleware.RequireAdmin(handler.UserSetDisabled(sc))))
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
