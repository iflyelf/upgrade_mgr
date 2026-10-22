// upgrade-agent 被控端入口。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentrun"
	"github.com/iflyelf/upgrade_mgr/internal/agent/installer"
	"github.com/iflyelf/upgrade_mgr/internal/agent/watchdog"
)

var (
	version = "0.1.0"
	variant = "" // legacy 构建由 Makefile 注入 -X main.variant=legacy
)

func main() {
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
	case "self-update", "upgrade", "status":
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
	serverURL := fs.String("server", "http://localhost:8090", "控制台地址")
	enrollToken := fs.String("enroll-token", "", "注册令牌")
	statePath := fs.String("state", "/var/lib/upgrade-agent/state.json", "状态文件路径")
	fs.Parse(os.Args[2:])

	cfg, err := agentrun.LoadState(*statePath)
	if err != nil && !os.IsNotExist(err) {
		// 文件缺失是首次安装的正常情况；内容损坏则提示运维重新安装。
		log.Printf("⚠️ 读取状态文件失败（将按新节点处理）: %v", err)
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = *serverURL
	}
	if cfg.EnrollToken == "" && cfg.AgentID == "" {
		cfg.EnrollToken = *enrollToken
	}
	cfg.StatePath = *statePath
	cfg.Version = version
	cfg.Variant = variant

	agent := agentrun.New(cfg)
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
	if err := agent.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("运行失败: %v", err)
	}
}

func usage() {
	fmt.Print(`upgrade-agent — 升级平台被控端

用法:
  upgrade-agent <command> [flags]

命令:
  run          常驻：注册/心跳/长轮询取指令/执行升级
  install      安装为服务（systemd/upstart/裸跑）并注册
  uninstall    卸载
  watchdog     保活看门狗（cron 调用）
  self-update  自升级（P3）
  upgrade      单机升级指定组件（P3）
  status       打印本机 OS/init/服务/版本（P3）
  version      显示版本
`)
}

func installCmd() {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	serverURL := fs.String("server", "", "控制台地址（必填）")
	enrollToken := fs.String("enroll-token", "", "注册令牌（可选，留空则提示手动）")
	binPath := fs.String("bin", "/usr/local/bin/upgrade-agent", "安装路径")
	statePath := fs.String("state", "/var/lib/upgrade-agent/state.json", "状态文件路径")
	installCron := fs.Bool("cron", true, "安装 cron 看门狗兜底")
	force := fs.Bool("force", false, "强制重新注册：覆盖已有 state 凭据（需 --enroll-token）")
	fs.Parse(os.Args[2:])
	if *serverURL == "" {
		fmt.Fprintln(os.Stderr, "错误: --server 必填")
		os.Exit(1)
	}
	opts := installer.Options{
		ServerURL: *serverURL, EnrollToken: *enrollToken,
		BinPath: *binPath, StatePath: *statePath, InstallCron: *installCron,
		Force: *force,
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
	statePath := fs.String("state", "/var/lib/upgrade-agent/state.json", "状态文件路径")
	binPath := fs.String("bin", "/usr/local/bin/upgrade-agent", "二进制路径")
	threshold := fs.Duration("threshold", 3*time.Minute, "心跳超时阈值")
	_ = fs.Bool("quiet", false, "静默模式（兼容 cron 调用）") // 兼容性参数，实际未使用
	fs.Parse(os.Args[2:])
	if err := watchdog.Check(*statePath, *binPath, *threshold); err != nil {
		log.Printf("watchdog: %v", err)
		os.Exit(1)
	}
}
