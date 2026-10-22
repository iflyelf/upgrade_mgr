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
	Arch    string `json:"arch"`
	Variant string `json:"variant,omitempty"` // "" 普通 / legacy
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	URL     string `json:"url"`
}

// Filename 返回二进制在 AGENT_BINARY_DIR 下的文件名。
func (t Target) Filename() string {
	name := "upgrade-agent-linux-" + t.Arch
	if t.Variant == "legacy" {
		name += "-legacy"
	}
	return name
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

// Resolve 定位二进制并计算 SHA256。
func (r *Resolver) Resolve(arch, variant, baseURL string) (Target, error) {
	arch, variant, err := NormalizeArchVariant(arch, variant)
	if err != nil {
		return Target{}, err
	}
	t := Target{Arch: arch, Variant: variant, Version: r.Version}
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
	t.URL = strings.TrimRight(baseURL, "/") + "/api/agent/binary/linux-" + arch
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
		if e.IsDir() || !strings.HasPrefix(name, "upgrade-agent-linux-") {
			continue
		}
		arch := strings.TrimPrefix(name, "upgrade-agent-linux-")
		variant := ""
		if strings.HasSuffix(arch, "-legacy") {
			arch = strings.TrimSuffix(arch, "-legacy")
			variant = "legacy"
		}
		t, err := r.Resolve(arch, variant, baseURL)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Arch == out[j].Arch {
			return out[i].Variant < out[j].Variant
		}
		return out[i].Arch < out[j].Arch
	})
	return out
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
