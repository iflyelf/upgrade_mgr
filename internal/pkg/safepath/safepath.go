// Package safepath 提供路径安全校验，防止路径遍历（../）攻击。
package safepath

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// segRe 允许的单段命名：字母/数字/点/下划线/中划线。不允许分隔符与空白。
var segRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// SafeSegment 校验单个路径段（文件名/版本/架构等），拒绝路径遍历与分隔符。
//
// 拒绝：空、"."、".."、含 "/"、"\\"、空白，以及非白名单字符。
func SafeSegment(s string) error {
	if s == "" {
		return fmt.Errorf("值不能为空")
	}
	if s == "." || s == ".." {
		return fmt.Errorf("非法值 %q", s)
	}
	if strings.ContainsAny(s, "/\\") {
		return fmt.Errorf("值 %q 不能包含路径分隔符", s)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("值 %q 不能包含 ..", s)
	}
	if !segRe.MatchString(s) {
		return fmt.Errorf("值 %q 含非法字符（仅允许字母/数字/._-）", s)
	}
	return nil
}

// SanitizeFilename 返回仅保留基名且经白名单过滤的安全文件名。
// 非法字符替换为 _；结果为空则返回 "file"。
func SanitizeFilename(name string) string {
	name = filepath.Base(filepath.FromSlash(name))
	name = strings.ReplaceAll(name, "\\", "_")
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.TrimLeft(b.String(), ".")
	if out == "" {
		return "file"
	}
	return out
}

// EnsureWithin 校验 target 解析后仍位于 base 目录内，防止越界读写。
func EnsureWithin(base, target string) error {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return fmt.Errorf("路径校验失败: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("路径 %q 越界（超出 %q）", target, base)
	}
	return nil
}
