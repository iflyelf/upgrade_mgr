// Package installer 全 Go 实现的 Agent 安装器：探测 init → 写服务/看门狗 → 启动。
//
// 不依赖任何外部 shell 脚本；服务模板以 go:embed 内嵌。
package installer

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/iflyelf/upgrade_mgr/internal/agent/osinfo"
)

//go:embed templates/*.tmpl
var tmplFS embed.FS

// Options 安装参数。
type Options struct {
	ServerURL   string
	EnrollToken string
	BinPath     string // 目标安装路径，默认 /usr/local/bin/upgrade-agent
	StatePath   string
	InstallCron bool // 是否安装 cron 看门狗兜底
	Force       bool // 强制重新注册：忽略已有 state 中的凭据并覆盖
}

// Install 执行安装：复制自身二进制 → 写服务单元 → 启用启动 → 可选 cron。
func Install(opts Options) error {
	if opts.BinPath == "" {
		opts.BinPath = "/usr/local/bin/upgrade-agent"
	}
	if opts.StatePath == "" {
		opts.StatePath = "/var/lib/upgrade-agent/state.json"
	}
	opts.ServerURL = strings.TrimRight(opts.ServerURL, "/")
	info := osinfo.Detect()

	// 1. 复制当前二进制到目标路径（若不同）
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取自身路径失败: %w", err)
	}
	if abs, _ := filepath.Abs(self); abs != opts.BinPath {
		if err := copyExecutable(self, opts.BinPath); err != nil {
			return fmt.Errorf("安装二进制失败: %w", err)
		}
	}
	// 强制重新注册必须携带新令牌；否则会覆盖旧凭据却无法注册。
	if opts.Force && opts.EnrollToken == "" {
		return fmt.Errorf("--force 需要同时提供 --enroll-token")
	}
	// 服务启动时只带 --server/--state，不带 --enroll-token；必须先把注册信息
	// 写入 0600 的 state，否则 Agent 每次启动都会用空令牌注册并返回 401。
	if err := writeInitialState(opts.StatePath, opts.ServerURL, opts.EnrollToken, opts.Force); err != nil {
		return fmt.Errorf("写入 Agent 注册状态失败: %w", err)
	}
	if opts.EnrollToken == "" {
		fmt.Println("⚠️ 未提供 --enroll-token；Agent 启动后会提示缺少注册令牌")
	}

	// 2. 按 init 类型写服务
	switch info.Init {
	case "systemd":
		if err := installSystemd(opts); err != nil {
			return err
		}
	case "upstart":
		if err := installUpstart(opts); err != nil {
			return err
		}
	default:
		fmt.Printf("⚠️ 未检测到 systemd/upstart，跳过服务安装（可用 cron 看门狗或裸跑）\n")
	}

	// 3. cron 看门狗兜底
	if opts.InstallCron {
		if err := installCron(opts.BinPath); err != nil {
			fmt.Printf("⚠️ 安装 cron 看门狗失败: %v\n", err)
		}
	}

	fmt.Printf("✅ 安装完成: init=%s bin=%s\n", info.Init, opts.BinPath)
	return nil
}

// persistedState 是 Agent state 文件的最小写入结构。
//
// 字段名必须与 agentrun.Config 的 JSON 键一致（Go 默认使用导出字段名），
// Agent 启动时由 agentrun.LoadState 读取。
type persistedState struct {
	ServerURL   string
	EnrollToken string
	AgentID     string
	Token       string
	Secret      string
	StatePath   string
}

// writeInitialState 在服务启动前写入 ServerURL/EnrollToken。
//
// 默认情况下，已注册且凭据完整的 state 不会被覆盖，避免重装二进制时丢失
// Agent 身份；force=true 时忽略旧凭据并重新注册（需同时提供 enrollToken）。
func writeInitialState(path, serverURL, enrollToken string, force bool) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// state 内含 token/secret，目录必须仅 root 可访问。
	// 相对路径（dir="."）或根目录不做 chmod，避免误改当前目录/系统根目录。
	if dir != "." && dir != string(filepath.Separator) {
		_ = os.Chmod(dir, 0700)
	}

	if !force {
		if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) > 0 {
			var existing persistedState
			if json.Unmarshal(b, &existing) == nil &&
				existing.AgentID != "" && existing.Token != "" && existing.Secret != "" {
				return nil
			}
		}
	}
	return writeStateAtomic(path, persistedState{
		ServerURL: serverURL, EnrollToken: enrollToken, StatePath: path,
	})
}

// writeStateAtomic 先写临时文件再 rename，避免进程中断留下空 state 文件。
func writeStateAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if f, err := os.OpenFile(tmp, os.O_RDWR, 0600); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func installSystemd(opts Options) error {
	content, err := render("upgrade-agent.service.tmpl", opts)
	if err != nil {
		return err
	}
	path := "/etc/systemd/system/upgrade-agent.service"
	if err := os.WriteFile(path, content, 0644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "upgrade-agent"); err != nil {
		return err
	}
	// 连续失败会触发 systemd start-limit，先清掉失败计数，保证重复安装能拉起。
	_ = run("systemctl", "reset-failed", "upgrade-agent")
	// 服务可能已在运行（例如重装/修复），必须显式 restart 才会加载新二进制和新 state。
	return run("systemctl", "restart", "upgrade-agent")
}

func installUpstart(opts Options) error {
	content, err := render("upgrade-agent.conf.tmpl", opts)
	if err != nil {
		return err
	}
	path := "/etc/init/upgrade-agent.conf"
	if err := os.WriteFile(path, content, 0644); err != nil {
		return err
	}
	// upstart 的 start 对已运行实例是空操作，先 stop 确保加载新二进制和新 state。
	_ = run("stop", "upgrade-agent")
	return run("start", "upgrade-agent")
}

func installCron(binPath string) error {
	line := fmt.Sprintf("* * * * * root %s watchdog\n", binPath)
	return os.WriteFile("/etc/cron.d/upgrade-agent", []byte(line), 0644)
}

// Uninstall 卸载服务与 cron。
func Uninstall() error {
	info := osinfo.Detect()
	switch info.Init {
	case "systemd":
		run("systemctl", "disable", "--now", "upgrade-agent")
		os.Remove("/etc/systemd/system/upgrade-agent.service")
		run("systemctl", "daemon-reload")
	case "upstart":
		run("stop", "upgrade-agent")
		os.Remove("/etc/init/upgrade-agent.conf")
	}
	os.Remove("/etc/cron.d/upgrade-agent")
	fmt.Println("✅ 已卸载")
	return nil
}

func render(name string, data any) ([]byte, error) {
	b, err := tmplFS.ReadFile("templates/" + name)
	if err != nil {
		return nil, err
	}
	t, err := template.New(name).Parse(string(b))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func copyExecutable(src, dst string) error {
	_ = os.MkdirAll(filepath.Dir(dst), 0755)
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// 原子写：先写临时文件再 rename
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, in, 0755); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func run(args ...string) error {
	if len(args) == 0 {
		return nil
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
