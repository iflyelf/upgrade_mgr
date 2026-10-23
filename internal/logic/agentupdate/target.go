// Package agentupdate 负责服务端 Agent 自升级：定位二进制、计算校验和、
// 下发 self_update 指令，并根据 Agent 上报的构建哈希判断是否需要升级。
package agentupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/safepath"
)

// Target 描述一个可下发的 Agent 二进制。
type Target struct {
	OS      string `json:"os"` // linux / windows
	Arch    string `json:"arch"`
	Variant string `json:"variant,omitempty"` // "" 普通 / legacy
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	URL     string `json:"url"`
}

// Filename 返回二进制在 AGENT_BINARY_DIR 下的文件名。
//
// 命名：upgrade-agent-<os>-<arch>[-legacy][.exe]
func (t Target) Filename() string {
	name := "upgrade-agent-" + t.DisplayOS() + "-" + t.Arch
	if t.Variant == "legacy" {
		name += "-legacy"
	}
	if t.DisplayOS() == OSWindows {
		name += ".exe"
	}
	return name
}

// DisplayOS 返回规范化的操作系统标识，空值按 linux 处理（兼容旧数据）。
func (t Target) DisplayOS() string {
	if t.OS == OSWindows {
		return OSWindows
	}
	return OSLinux
}

const (
	OSLinux   = "linux"
	OSWindows = "windows"
)

var validOS = map[string]bool{
	OSLinux:   true,
	OSWindows: true,
}

// NormalizeOS 校验并规范化目标操作系统标识。
func NormalizeOS(os string) (string, error) {
	os = strings.TrimSpace(strings.ToLower(os))
	if os == "" {
		return OSLinux, nil
	}
	if !validOS[os] {
		return "", fmt.Errorf("不支持的 Agent 操作系统 %q", os)
	}
	return os, nil
}

// NormalizeArchVariantForOS 在指定 OS 上校验架构/变体。
//
// Windows 只有 amd64 / arm64（且没有 legacy 构建：Go 1.20 同时覆盖
// Server 2008 R2 到 Windows 11）；Linux 保留原有 amd64/arm64/arm/386。
func NormalizeArchVariantForOS(os, arch, variant string) (string, string, error) {
	arch, variant, err := NormalizeArchVariant(arch, variant)
	if err != nil {
		return "", "", err
	}
	if os == OSWindows {
		if variant == "legacy" {
			return "", "", fmt.Errorf("Windows 不支持 legacy 构建")
		}
		if arch != "amd64" && arch != "arm64" {
			return "", "", fmt.Errorf("Windows 仅支持 amd64/arm64，收到 %q", arch)
		}
	}
	return arch, variant, nil
}

// ChooseOS 根据主机上报的 os_family 判断需要下发哪种 Agent。
//
// Agent 上报的 os_family 在 Windows 上固定为 windows；Linux 为
// rhel/debian/suse/alpine/linux 等。历史主机没有该字段时按 Linux 处理。
func ChooseOS(osFamily string) string {
	if strings.EqualFold(strings.TrimSpace(osFamily), OSWindows) {
		return OSWindows
	}
	return OSLinux
}

type hashEntry struct {
	size int64
	mod  int64
	sum  string
}

var hashCache sync.Map // path -> hashEntry

// Resolver 按目录与版本解析 Agent 二进制。
type Resolver struct {
	Dir     string
	Version string
}

// NewResolver 创建解析器。version 为空时使用 dev，避免页面展示空版本。
func NewResolver(dir, version string) *Resolver {
	if strings.TrimSpace(dir) == "" {
		dir = "./dist"
	}
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	return &Resolver{Dir: dir, Version: version}
}

var validArches = map[string]bool{
	"amd64": true, "arm64": true, "arm": true, "386": true,
}

// NormalizeArchVariant 校验并拆分架构/变体；允许 arch 自带 -legacy 后缀。
func NormalizeArchVariant(arch, variant string) (string, string, error) {
	arch = strings.TrimSpace(strings.ToLower(arch))
	variant = strings.TrimSpace(strings.ToLower(variant))
	if strings.HasSuffix(arch, "-legacy") {
		arch = strings.TrimSuffix(arch, "-legacy")
		variant = "legacy"
	}
	if arch == "" {
		return "", "", fmt.Errorf("缺少 Agent 架构")
	}
	if !validArches[arch] {
		return "", "", fmt.Errorf("不支持的 Agent 架构 %q", arch)
	}
	switch variant {
	case "", "legacy":
	default:
		return "", "", fmt.Errorf("不支持的 Agent 变体 %q", variant)
	}
	return arch, variant, nil
}

// ChooseVariant 决定旧 Agent 应升级到哪种构建。
//
// 新 Agent 会直接上报 legacy；旧 Agent 没有该字段，只能依据内核判断：
// 2.x 内核（CentOS 6 等）使用 legacy 构建，其余使用普通构建。
func ChooseVariant(reported, kernel string) string {
	reported = strings.TrimSpace(strings.ToLower(reported))
	if reported == "legacy" {
		return "legacy"
	}
	if reported != "" {
		return ""
	}
	if strings.HasPrefix(strings.TrimSpace(kernel), "2.") {
		return "legacy"
	}
	return ""
}

// Resolve 定位 Linux Agent 二进制并计算 SHA256（兼容旧调用方）。
func (r *Resolver) Resolve(arch, variant, baseURL string) (Target, error) {
	return r.ResolveOS(OSLinux, arch, variant, baseURL)
}

// ResolveOS 定位指定操作系统的 Agent 二进制并计算 SHA256。
func (r *Resolver) ResolveOS(osName, arch, variant, baseURL string) (Target, error) {
	osName, err := NormalizeOS(osName)
	if err != nil {
		return Target{}, err
	}
	arch, variant, err = NormalizeArchVariantForOS(osName, arch, variant)
	if err != nil {
		return Target{}, err
	}
	t := Target{OS: osName, Arch: arch, Variant: variant, Version: r.Version}
	path := filepath.Join(r.Dir, t.Filename())
	if err := safepath.EnsureWithin(r.Dir, path); err != nil {
		return Target{}, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return Target{}, fmt.Errorf("二进制 %s 不可用: %w", t.Filename(), err)
	}
	if !fi.Mode().IsRegular() {
		return Target{}, fmt.Errorf("二进制 %s 不是常规文件", t.Filename())
	}
	sum, err := fileSHA256(path, fi)
	if err != nil {
		return Target{}, fmt.Errorf("计算 %s 校验和失败: %w", t.Filename(), err)
	}
	t.SHA256 = sum
	t.Size = fi.Size()
	t.URL = strings.TrimRight(baseURL, "/") + "/api/agent/binary/" + osName + "-" + arch
	if variant == "legacy" {
		t.URL += "-legacy"
	}
	return t, nil
}

// Available 返回目录下全部可下发的 Agent 二进制，供管理页面展示。
func (r *Resolver) Available(baseURL string) []Target {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return nil
	}
	out := make([]Target, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		osName, arch, variant, ok := parseBinaryName(name)
		if !ok {
			continue
		}
		t, err := r.ResolveOS(osName, arch, variant, baseURL)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OS != out[j].OS {
			return out[i].OS < out[j].OS
		}
		if out[i].Arch != out[j].Arch {
			return out[i].Arch < out[j].Arch
		}
		return out[i].Variant < out[j].Variant
	})
	return out
}

// parseBinaryName 解析 upgrade-agent-<os>-<arch>[-legacy][.exe]。
func parseBinaryName(name string) (osName, arch, variant string, ok bool) {
	const prefix = "upgrade-agent-"
	if !strings.HasPrefix(name, prefix) {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(name, prefix)
	rest = strings.TrimSuffix(rest, ".exe")
	for _, cand := range []string{OSWindows, OSLinux} {
		if !strings.HasPrefix(rest, cand+"-") {
			continue
		}
		osName = cand
		arch = strings.TrimPrefix(rest, cand+"-")
		break
	}
	if osName == "" {
		return "", "", "", false
	}
	if strings.HasSuffix(arch, "-legacy") {
		arch = strings.TrimSuffix(arch, "-legacy")
		variant = "legacy"
	}
	return osName, arch, variant, true
}

func fileSHA256(path string, fi os.FileInfo) (string, error) {
	key := path
	if v, ok := hashCache.Load(key); ok {
		if e, ok := v.(hashEntry); ok && e.size == fi.Size() && e.mod == fi.ModTime().UnixNano() {
			return e.sum, nil
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	hashCache.Store(key, hashEntry{size: fi.Size(), mod: fi.ModTime().UnixNano(), sum: sum})
	return sum, nil
}
