package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoHardcodedExternalURL 平台对外地址不允许出现在源码/脚本/配置/文档中。
//
// external_url 必须来自设置页/环境变量，安装命令由服务端动态拼接；
// 防止以后又把具体域名写死到安装器、脚本或页面。
func TestNoHardcodedExternalURL(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	banned := "mgrup" + ".example.com" // 拆开写，避免扫描器命中本文件自身
	textExt := map[string]bool{
		".go": true, ".js": true, ".css": true, ".sh": true, ".ps1": true,
		".yaml": true, ".yml": true, ".md": true, ".json": true, ".sql": true,
		".html": true, ".conf": true,
	}
	skipDir := map[string]bool{
		".git": true, "node_modules": true, "test-results": true, "bin": true,
	}
	var hits []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 2<<20 {
			return nil
		}
		name := d.Name()
		if !textExt[strings.ToLower(filepath.Ext(name))] && name != "Makefile" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(data), banned) {
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("发现硬编码的平台地址 %s，请改为 external_url/--server/环境变量：%v",
			banned, hits)
	}
}
