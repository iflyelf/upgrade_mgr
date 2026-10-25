// Package installer 全 Go 实现的 Agent 安装器：探测平台 → 写服务 → 启动。
//
// Linux：systemd / upstart + cron 看门狗；
// Windows：SCM 服务（含失败自动重启）+ 可选计划任务看门狗。
// 不依赖任何外部 shell 脚本；服务模板以 go:embed 内嵌。
package installer

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentpath"
	"github.com/iflyelf/upgrade_mgr/internal/agent/osinfo"
)

//go:embed templates/*.tmpl
var tmplFS embed.FS

// ServiceName 是 Agent 在目标机注册的服务名。
// Linux systemd/upstart 与 Windows SCM 使用同一名称，便于运维排查。
const ServiceName = "upgrade-agent"

// Options 安装参数。
type Options struct {
	ServerURL string
	// ServerURLs 备用连接地址（含 ServerURL 本身）。安装时写入 state，
	// Agent 默认地址不可达时按顺序轮换。
	ServerURLs  []string
	EnrollToken string
	BinPath     string // 目标安装路径，留空使用平台默认值
	StatePath   string
	InstallCron bool // 是否安装看门狗兜底（Linux cron / Windows 计划任务）
	Force       bool // 强制重新注册：忽略已有 state 中的凭据并覆盖
	// 可选 TLS / 双向 TLS：仅写入 state，由 Agent 启动时读取。
	TLSCA   string
	TLSCert string
	TLSKey  string
	// ServiceAccount / ServicePassword 可选：Windows 服务运行账户。
	// 留空使用 LocalSystem；使用低权限账户前需先授予「作为服务登录」权限。
	ServiceAccount  string
	ServicePassword string
}

// Install 执行安装：复制自身二进制 → 写服务单元 → 启用启动 → 可选看门狗。
func Install(opts Options) error {
	if opts.BinPath == "" {
		opts.BinPath = agentpath.BinPath()
	}
	if opts.StatePath == "" {
		opts.StatePath = agentpath.StatePath()
	}
	opts.ServerURL = strings.TrimRight(opts.ServerURL, "/")
	// 服务单元模板会把这些值直接写入 ExecStart；拒绝换行等控制字符，
	// 防止构造安装参数时向 unit 文件注入额外指令。
	for name, v := range map[string]string{
		"server": opts.ServerURL, "bin": opts.BinPath, "state": opts.StatePath,
	} {
		if strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("%s 参数包含非法控制字符", name)
		}
	}
	if opts.ServerURL != "" {
		if strings.ContainsAny(opts.ServerURL, " \t\"'\\") {
			return fmt.Errorf("server 地址包含空白或引号")
		}
		u, err := url.Parse(opts.ServerURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("server 必须是 http(s) 绝对地址: %q", opts.ServerURL)
		}
	}
	info := osinfo.Detect()

	// 0. 平台预处理：Windows 上先停掉旧服务，避免安装时 exe 被占用。
	prepareInstall(opts)

	// 1. 复制当前二进制到目标路径（若不同）
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取自身路径失败: %w", err)
	}
	if abs, _ := filepath.Abs(self); !samePath(abs, opts.BinPath) {
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
	if err := writeInitialState(opts); err != nil {
		return fmt.Errorf("写入 Agent 注册状态失败: %w", err)
	}
	if opts.EnrollToken == "" {
		fmt.Println("⚠️ 未提供 --enroll-token；Agent 启动后会提示缺少注册令牌")
	}

	// 2. 按平台写服务
	if err := installPlatform(opts, info); err != nil {
		return err
	}

	// 3. 看门狗兜底
	if opts.InstallCron {
		if err := installWatchdog(opts.BinPath, opts.StatePath); err != nil {
			fmt.Printf("⚠️ 安装看门狗失败: %v\n", err)
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
	ServerURL string
	// DefaultServerURL 记录安装时的默认地址：连接地址被覆盖后 Agent 切换，
	// 服务端清空覆盖时据此回退。
	DefaultServerURL string
	// ServerURLs 备用连接地址（含默认地址），用于默认入口不可达时自动轮换。
	ServerURLs  []string `json:",omitempty"`
	EnrollToken string
	AgentID     string
	Token       string
	Secret      string
	StatePath   string
	// 可选 TLS / 双向 TLS。
	TLSCA   string `json:",omitempty"`
	TLSCert string `json:",omitempty"`
	TLSKey  string `json:",omitempty"`
}

// writeInitialState 在服务启动前写入 ServerURL/EnrollToken。
//
// 默认情况下，已注册且凭据完整的 state 不会被覆盖，避免重装二进制时丢失
// Agent 身份；force=true 时忽略旧凭据并重新注册（需同时提供 enrollToken）。
func writeInitialState(opts Options) error {
	path := opts.StatePath
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	hardenStateDir(dir)

	if !opts.Force {
		if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) > 0 {
			var existing persistedState
			if json.Unmarshal(b, &existing) == nil &&
				existing.AgentID != "" && existing.Token != "" && existing.Secret != "" {
				// 已注册：保留凭据，但允许本次安装更新 TLS 参数（重装时补证书）。
				if opts.TLSCA != "" || opts.TLSCert != "" || opts.TLSKey != "" {
					existing.TLSCA = firstNonEmpty(opts.TLSCA, existing.TLSCA)
					existing.TLSCert = firstNonEmpty(opts.TLSCert, existing.TLSCert)
					existing.TLSKey = firstNonEmpty(opts.TLSKey, existing.TLSKey)
					return writeStateAtomic(path, existing)
				}
				return nil
			}
		}
	}
	urls := opts.ServerURLs
	if len(urls) == 0 {
		urls = []string{opts.ServerURL}
	}
	return writeStateAtomic(path, persistedState{
		ServerURL: opts.ServerURL, DefaultServerURL: opts.ServerURL,
		ServerURLs:  urls,
		EnrollToken: opts.EnrollToken, StatePath: path,
		TLSCA: opts.TLSCA, TLSCert: opts.TLSCert, TLSKey: opts.TLSKey,
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

// firstNonEmpty 返回第一个非空值。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	// 目标就是当前运行中的二进制（Windows 上无法覆盖），直接跳过。
	if cur, err := os.Executable(); err == nil && samePath(cur, dst) {
		return nil
	}
	// Windows 上目标可能正被旧服务占用；先停服务（平台实现在 Install 前已处理），
	// 仍占用时改名让位，避免「拒绝访问」。
	if err := copyExecutablePlatform(src, dst); err != nil {
		return err
	}
	return nil
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
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

// Uninstall 卸载服务与看门狗。
func Uninstall() error {
	info := osinfo.Detect()
	if err := uninstallPlatform(info); err != nil {
		return err
	}
	removeWatchdog()
	fmt.Println("✅ 已卸载")
	return nil
}
