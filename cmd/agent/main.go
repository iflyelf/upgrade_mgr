// upgrade-agent 被控端入口。
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

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentpath"
	"github.com/iflyelf/upgrade_mgr/internal/agent/agentrun"
	"github.com/iflyelf/upgrade_mgr/internal/agent/binswap"
	"github.com/iflyelf/upgrade_mgr/internal/agent/diagnose"
	"github.com/iflyelf/upgrade_mgr/internal/agent/installer"
	"github.com/iflyelf/upgrade_mgr/internal/agent/tlsclient"
	"github.com/iflyelf/upgrade_mgr/internal/agent/watchdog"
	"github.com/iflyelf/upgrade_mgr/internal/agent/winsvc"
)

var (
	version = "0.1.0"
	variant = "" // legacy 构建由 Makefile 注入 -X main.variant=legacy
)

func main() {
	// Windows 自升级会把旧 exe 改名成 .old；新版本启动时旧进程已退出，可安全清理。
	if exe, err := os.Executable(); err == nil {
		binswap.CleanupOld(exe)
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version", "-version", "--version":
		fmt.Printf("upgrade-agent version %s\n", version)
	case "help", "-h", "--help":
		usage()
	case "run":
		runCmd()
	case "install":
		installCmd()
	case "uninstall":
		uninstallCmd()
	case "watchdog":
		watchdogCmd()
	case "status":
		statusCmd()
	case "diagnose":
		diagnoseCmd()
	case "self-update", "upgrade":
		fmt.Printf("子命令 %s 将在后续版本实现\n", os.Args[1])
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func runCmd() {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	serverURL := fs.String("server", "http://localhost:8090", "控制台地址；可逗号分隔多个作为备用")
	enrollToken := fs.String("enroll-token", "", "注册令牌")
	statePath := fs.String("state", agentpath.StatePath(), "状态文件路径")
	// --service 由 Windows 安装器写入服务命令行；Linux 上忽略。
	_ = fs.Bool("service", false, "由 Windows SCM 启动（服务模式）")
	tlsCA := fs.String("tls-ca", "", "可选：校验服务端证书的 CA 文件（PEM）")
	tlsCert := fs.String("tls-cert", "", "可选：客户端证书（双向 TLS）")
	tlsKey := fs.String("tls-key", "", "可选：客户端私钥（双向 TLS）")
	fs.Parse(os.Args[2:])

	cfg, err := agentrun.LoadState(*statePath)
	if err != nil && !os.IsNotExist(err) {
		// 文件缺失是首次安装的正常情况；内容损坏则提示运维重新安装。
		log.Printf("⚠️ 读取状态文件失败（将按新节点处理）: %v", err)
	}
	// --server 支持逗号分隔的多个地址：第一个为当前地址，其余作为备用。
	if cfg.ServerURL == "" {
		cfg.ServerURL = splitServers(*serverURL)[0]
	}
	if len(cfg.ServerURLs) == 0 {
		cfg.ServerURLs = splitServers(*serverURL)
	}
	if cfg.DefaultServerURL == "" {
		// 旧 state / 手工 run 没有该字段：把当前地址作为默认地址，
		// 隧道切换失败或隧道被清空时可回退。
		cfg.DefaultServerURL = cfg.ServerURL
	}
	if cfg.EnrollToken == "" && cfg.AgentID == "" {
		cfg.EnrollToken = *enrollToken
	}
	cfg.StatePath = *statePath
	cfg.Version = version
	cfg.Variant = variant
	// 命令行参数优先于 state 中保存的值（便于临时调试/切换证书）。
	if *tlsCA != "" {
		cfg.TLSCA = *tlsCA
	}
	if *tlsCert != "" {
		cfg.TLSCert = *tlsCert
	}
	if *tlsKey != "" {
		cfg.TLSKey = *tlsKey
	}
	// 制品下载走 downloader 的 http.DefaultClient；配置了 TLS 时统一替换，
	// 保证自升级/组件下载同样使用客户端证书与私有 CA。
	if client, err := tlsclient.New(tlsclient.Config{
		CAFile: cfg.TLSCA, CertFile: cfg.TLSCert, KeyFile: cfg.TLSKey,
	}); err != nil {
		log.Printf("⚠️ TLS 客户端配置失败: %v", err)
	} else if client != nil {
		http.DefaultClient = client
	}

	run := func(ctx context.Context) error {
		release, err := waitRunLock(ctx)
		if err != nil {
			return nil // 等锁期间收到停止信号，属正常退出
		}
		defer release()
		agent := agentrun.New(cfg)
		return agent.Run(ctx)
	}

	// Windows：若由 SCM 启动，转为服务模式运行（阻塞到服务停止）。
	if handled, err := winsvc.RunIfService(installer.ServiceName, run); handled {
		if err != nil {
			log.Fatalf("服务运行失败: %v", err)
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		log.Println("收到信号，退出...")
		cancel()
	}()

	log.Printf("upgrade-agent %s 启动", version)
	if err := run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("运行失败: %v", err)
	}
}

// waitRunLock 保证同一主机只有一个 Agent 常驻进程。
//
// 已有实例时阻塞等待而不是退出：systemd/upstart 下退出会触发 3 秒一次的重启风暴；
// 旧实例退出（如自升级 exit(75)）后本进程立即接管。锁目录异常时不阻塞启动。
func waitRunLock(ctx context.Context) (func(), error) {
	logged := false
	for {
		release, ok, err := watchdog.AcquireRunLock()
		if err != nil {
			log.Printf("⚠️ 获取 Agent 运行锁失败（继续启动，无法防止重复实例）: %v", err)
			return func() {}, nil
		}
		if ok {
			return release, nil
		}
		if !logged {
			log.Printf("已有 upgrade-agent 进程在运行，等待其退出后再启动")
			logged = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func usage() {
	fmt.Print(`upgrade-agent — 升级平台被控端

用法:
  upgrade-agent <command> [flags]

命令:
  run          常驻：注册/心跳/长轮询取指令/执行升级
  install      安装为服务（Linux systemd/upstart、Windows SCM）并注册
  uninstall    卸载
  watchdog     保活看门狗（cron / 计划任务调用）
  status       打印连接地址、备用列表、最近切换原因等排查信息
  diagnose     主动拨测服务端链路（DNS/TCP/TLS/HTTP，可选鉴权心跳）
  self-update  自升级（P3）
  upgrade      单机升级指定组件（P3）
  version      显示版本
`)
}

func installCmd() {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	serverURL := fs.String("server", "", "控制台地址（必填）；可逗号分隔多个作为备用，如 https://a,https://b")
	enrollToken := fs.String("enroll-token", "", "注册令牌（可选，留空则提示手动）")
	binPath := fs.String("bin", agentpath.BinPath(), "安装路径")
	statePath := fs.String("state", agentpath.StatePath(), "状态文件路径")
	installCron := fs.Bool("cron", true, "安装看门狗兜底（Linux cron / Windows 计划任务）")
	force := fs.Bool("force", false, "强制重新注册：覆盖已有 state 凭据（需 --enroll-token）")
	tlsCA := fs.String("tls-ca", "", "可选：校验服务端证书的 CA 文件（PEM）")
	tlsCert := fs.String("tls-cert", "", "可选：客户端证书（双向 TLS）")
	tlsKey := fs.String("tls-key", "", "可选：客户端私钥（双向 TLS）")
	serviceAccount := fs.String("service-account", "", "可选：Windows 服务运行账户（留空为 LocalSystem）")
	servicePassword := fs.String("service-password", "", "可选：服务账户密码（gMSA 可留空）")
	fs.Parse(os.Args[2:])
	if *serverURL == "" {
		fmt.Fprintln(os.Stderr, "错误: --server 必填")
		os.Exit(1)
	}
	opts := installer.Options{
		ServerURL: *serverURL, ServerURLs: splitServers(*serverURL), EnrollToken: *enrollToken,
		BinPath: *binPath, StatePath: *statePath, InstallCron: *installCron,
		Force: *force, TLSCA: *tlsCA, TLSCert: *tlsCert, TLSKey: *tlsKey,
		ServiceAccount: *serviceAccount, ServicePassword: *servicePassword,
	}
	if err := installer.Install(opts); err != nil {
		log.Fatalf("安装失败: %v", err)
	}
}

func uninstallCmd() {
	if err := installer.Uninstall(); err != nil {
		log.Fatalf("卸载失败: %v", err)
	}
}

func watchdogCmd() {
	fs := flag.NewFlagSet("watchdog", flag.ExitOnError)
	statePath := fs.String("state", agentpath.StatePath(), "状态文件路径")
	binPath := fs.String("bin", agentpath.BinPath(), "二进制路径")
	threshold := fs.Duration("threshold", 3*time.Minute, "心跳超时阈值")
	_ = fs.Bool("quiet", false, "静默模式（兼容 cron 调用）") // 兼容性参数，实际未使用
	fs.Parse(os.Args[2:])
	if err := watchdog.Check(*statePath, *binPath, *threshold); err != nil {
		log.Printf("watchdog: %v", err)
		os.Exit(1)
	}
}

// splitServers 拆分逗号/空白分隔的服务端地址列表。
//
// 支持 `--server https://a:8090,https://b:8090`，便于部署多入口/隧道场景
// 时让 Agent 自动故障切换；返回值至少包含一个元素（可能为空串）。
func splitServers(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// statusCmd 输出 Agent 当前连接状态，便于现场排查地址/隧道切换问题。
//
// 只读取 state 与本地探测，不访问服务端，因此离线也能用。
func statusCmd() {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	statePath := fs.String("state", agentpath.StatePath(), "状态文件路径")
	asJSON := fs.Bool("json", false, "以 JSON 输出（便于脚本采集）")
	fs.Parse(os.Args[2:])

	st, err := agentrun.BuildStatus(*statePath, version, variant)
	if err != nil {
		log.Fatalf("读取状态失败: %v", err)
	}
	if *asJSON {
		fmt.Println(st.FormatJSON())
		return
	}
	fmt.Print(st.FormatText())
	if !st.Registered {
		os.Exit(2)
	}
}

// diagnoseCmd 主动诊断到服务端的完整链路，输出各阶段耗时。
//
// 与 status 的区别：status 只读本地 state；diagnose 会真实发起网络请求。
func diagnoseCmd() {
	fs := flag.NewFlagSet("diagnose", flag.ExitOnError)
	statePath := fs.String("state", agentpath.StatePath(), "状态文件路径")
	serverURL := fs.String("server", "", "覆盖待测试地址（不写回 state）")
	timeout := fs.Duration("timeout", diagnose.DefaultTimeout, "单个网络阶段超时")
	heartbeat := fs.Bool("heartbeat", false, "执行一次带凭据心跳（会更新服务端 last_seen，可能触发自动升级）")
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	fs.Parse(os.Args[2:])

	res, err := diagnose.Run(diagnose.Options{
		StatePath: *statePath, ServerURL: *serverURL, Timeout: *timeout,
		Heartbeat: *heartbeat, Version: version, Variant: variant,
	})
	if err != nil {
		log.Fatalf("诊断失败: %v", err)
	}
	if *asJSON {
		fmt.Println(res.FormatJSON())
	} else {
		fmt.Print(res.FormatText())
	}
	if !res.OK {
		os.Exit(1)
	}
}
