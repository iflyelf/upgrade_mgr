// Package installer 全 Go 实现的 Agent 安装器：探测 init → 写服务/看门狗 → 启动。
//
// 不依赖任何外部 shell 脚本；服务模板以 go:embed 内嵌。
package installer

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
}

// Install 执行安装：复制自身二进制 → 写服务单元 → 启用启动 → 可选 cron。
func Install(opts Options) error {
	if opts.BinPath == "" {
		opts.BinPath = "/usr/local/bin/upgrade-agent"
	}
	if opts.StatePath == "" {
		opts.StatePath = "/var/lib/upgrade-agent/state.json"
	}
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
	_ = os.MkdirAll(filepath.Dir(opts.StatePath), 0755)

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

func installSystemd(opts Options) error {
	content, err := render("upgrade-agent.service.tmpl", opts)
	if err != nil {
		return err
	}
	path := "/etc/systemd/system/upgrade-agent.service"
	if err := os.WriteFile(path, content, 0644); err != nil {
		return err
	}
	run("systemctl", "daemon-reload")
	return run("systemctl", "enable", "--now", "upgrade-agent")
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
