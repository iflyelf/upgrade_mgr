// Package skills 内嵌 Agent Skills（SKILL.md 规范，兼容 OpenCode / Claude Code / Codex），
// 下载时按当前设置渲染 MCP 服务名、地址与令牌环境变量，避免把部署信息写死在文件里。
package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed catalog
var catalogFS embed.FS

const (
	catalogRoot = "catalog"
	skillFile   = "SKILL.md"
	maxDescLen  = 1024
	maxNameLen  = 64
)

// NamePattern Agent Skills 规范的名称规则。
var NamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Data 渲染变量（全部来自运行时设置）。
type Data struct {
	ServerName string
	MCPURL     string
	BaseURL    string
	TokenEnv   string
}

// Meta Skill 元数据（取自 SKILL.md frontmatter）。
type Meta struct {
	Name          string            `json:"name" yaml:"name"`
	Description   string            `json:"description" yaml:"description"`
	License       string            `json:"license,omitempty" yaml:"license"`
	Compatibility string            `json:"compatibility,omitempty" yaml:"compatibility"`
	Metadata      map[string]string `json:"metadata,omitempty" yaml:"metadata"`
	Files         []string          `json:"files"`
}

// Names 返回内嵌的全部 Skill 名称（目录名，已排序）。
func Names() []string {
	entries, err := fs.ReadDir(catalogFS, catalogRoot)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Exists 判断 Skill 是否存在。
func Exists(name string) bool {
	if !NamePattern.MatchString(name) {
		return false
	}
	_, err := fs.Stat(catalogFS, path.Join(catalogRoot, name, skillFile))
	return err == nil
}

// Files 渲染某个 Skill 的全部文件（相对路径 → 内容）。
func Files(name string, d Data) (map[string][]byte, error) {
	if !Exists(name) {
		return nil, fmt.Errorf("Skill %s 不存在", name)
	}
	root := path.Join(catalogRoot, name)
	out := map[string][]byte{}
	err := fs.WalkDir(catalogFS, root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(catalogFS, p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, root+"/")
		rendered, err := render(rel, raw, d)
		if err != nil {
			return err
		}
		out[rel] = rendered
		return nil
	})
	return out, err
}

func render(name string, raw []byte, d Data) ([]byte, error) {
	if !strings.HasSuffix(name, ".md") {
		return raw, nil
	}
	tpl, err := template.New(name).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("解析模板 %s 失败: %w", name, err)
	}
	var b bytes.Buffer
	if err := tpl.Execute(&b, d); err != nil {
		return nil, fmt.Errorf("渲染 %s 失败: %w", name, err)
	}
	return b.Bytes(), nil
}

// ParseMeta 解析渲染后的 SKILL.md frontmatter。
func ParseMeta(content []byte) (*Meta, error) {
	s := strings.ReplaceAll(string(content), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, fmt.Errorf("SKILL.md 缺少 YAML frontmatter")
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return nil, fmt.Errorf("SKILL.md frontmatter 未闭合")
	}
	var m Meta
	if err := yaml.Unmarshal([]byte(s[4:4+end]), &m); err != nil {
		return nil, fmt.Errorf("frontmatter 解析失败: %w", err)
	}
	return &m, nil
}

// Validate 按 Agent Skills 规范校验元数据（名称与目录一致、长度限制）。
func Validate(dir string, m *Meta) error {
	if m.Name == "" || len(m.Name) > maxNameLen || !NamePattern.MatchString(m.Name) {
		return fmt.Errorf("Skill 名称 %q 不符合规范", m.Name)
	}
	if m.Name != dir {
		return fmt.Errorf("Skill 名称 %q 与目录 %q 不一致", m.Name, dir)
	}
	if l := len([]rune(strings.TrimSpace(m.Description))); l == 0 || l > maxDescLen {
		return fmt.Errorf("Skill %s 描述长度应为 1-%d 字符，当前 %d", m.Name, maxDescLen, l)
	}
	return nil
}

// Catalog 列出全部 Skill 元数据（已渲染）。
func Catalog(d Data) ([]Meta, error) {
	out := []Meta{}
	for _, name := range Names() {
		files, err := Files(name, d)
		if err != nil {
			return nil, err
		}
		m, err := ParseMeta(files[skillFile])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := Validate(name, m); err != nil {
			return nil, err
		}
		for f := range files {
			m.Files = append(m.Files, f)
		}
		sort.Strings(m.Files)
		out = append(out, *m)
	}
	return out, nil
}

// Archive 打包为 tar.gz，顶层目录为 Skill 名称（解压到 skills 目录即可）。
func Archive(name string, d Data, modTime time.Time) ([]byte, error) {
	files, err := Files(name, d)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dirs := map[string]bool{}
	writeDir := func(p string) error {
		if dirs[p] {
			return nil
		}
		dirs[p] = true
		return tw.WriteHeader(&tar.Header{Name: p + "/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: modTime})
	}
	if err := writeDir(name); err != nil {
		return nil, err
	}
	for _, k := range keys {
		full := path.Join(name, k)
		if d := path.Dir(full); d != name {
			if err := writeDir(d); err != nil {
				return nil, err
			}
		}
		body := files[k]
		if err := tw.WriteHeader(&tar.Header{Name: full, Mode: 0o644, Size: int64(len(body)), ModTime: modTime}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
