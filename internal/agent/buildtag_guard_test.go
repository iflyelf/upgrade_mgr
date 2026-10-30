package agent_test

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// nonWindowsGOOS 文件名后缀为这些系统时，Windows 构建不会编译该文件。
var nonWindowsGOOS = []string{"linux", "darwin", "freebsd", "netbsd", "openbsd", "solaris", "aix", "android", "illumos"}

// builtOnWindows 判断测试文件在 Windows 构建时是否会被编译（文件名后缀 + //go:build 约束）。
func builtOnWindows(name string, f *ast.File) bool {
	base := strings.TrimSuffix(name, "_test.go")
	for _, goos := range nonWindowsGOOS {
		if strings.HasSuffix(base, "_"+goos) {
			return false
		}
	}
	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			break
		}
		for _, c := range cg.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				return true
			}
			return expr.Eval(func(tag string) bool {
				return tag == "windows" || tag == "amd64" || strings.HasPrefix(tag, "go1.")
			})
		}
	}
	return true
}

// topLevelNames 返回文件中的顶层函数/类型/变量/常量名（方法除外）。
func topLevelNames(f *ast.File) []string {
	var out []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				out = append(out, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					out = append(out, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						out = append(out, n.Name)
					}
				}
			}
		}
	}
	return out
}

// TestWindowsTestFilesDoNotUseUnixOnlyHelpers 防止 Windows E2E 编译失败的回归。
//
// 例如 executor 的 newCmd / fakeMgr 定义在 //go:build !windows 的 service_test.go 中；
// 新测试文件引用它们却忘了加同样的构建标签，Linux 上一切正常，Windows CI 才报 undefined。
// 本测试在 Linux 上就能发现：Windows 会编译的测试文件不得引用只在非 Windows 测试文件中定义的标识符。
func TestWindowsTestFilesDoNotUseUnixOnlyHelpers(t *testing.T) {
	dirs := map[string]bool{}
	if err := filepath.Walk(".", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(p, "_test.go") {
			dirs[filepath.Dir(p)] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("未找到任何测试文件，目录结构可能已变化，请同步更新本测试")
	}

	for dir := range dirs {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return strings.HasSuffix(fi.Name(), "_test.go")
		}, parser.ParseComments)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", dir, err)
		}
		for _, pkg := range pkgs {
			winFiles := map[string]*ast.File{}
			winDecl := map[string]bool{}
			unixDecl := map[string]string{} // 标识符 → 定义所在文件
			for path, f := range pkg.Files {
				name := filepath.Base(path)
				if builtOnWindows(name, f) {
					winFiles[name] = f
					for _, n := range topLevelNames(f) {
						winDecl[n] = true
					}
				} else {
					for _, n := range topLevelNames(f) {
						unixDecl[n] = name
					}
				}
			}
			for name, f := range winFiles {
				used := map[string]bool{}
				ast.Inspect(f, func(n ast.Node) bool {
					// 选择器右侧（x.Name）是字段/方法，不是包级标识符引用。
					if sel, ok := n.(*ast.SelectorExpr); ok {
						ast.Inspect(sel.X, func(m ast.Node) bool {
							if id, ok := m.(*ast.Ident); ok {
								used[id.Name] = true
							}
							return true
						})
						return false
					}
					if id, ok := n.(*ast.Ident); ok {
						used[id.Name] = true
					}
					return true
				})
				var bad []string
				for id := range used {
					if src, ok := unixDecl[id]; ok && !winDecl[id] {
						bad = append(bad, id+"（定义于 "+src+"）")
					}
				}
				sort.Strings(bad)
				if len(bad) > 0 {
					t.Errorf("%s/%s 在 Windows 上会编译，但引用了只在非 Windows 测试文件中定义的 %s；"+
						"请给该文件加 //go:build !windows，或把辅助函数移到不带构建标签的文件",
						dir, name, strings.Join(bad, "、"))
				}
			}
		}
	}
}
