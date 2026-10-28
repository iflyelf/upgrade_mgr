package handler

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/examples"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
)

// seedEmbeddedScript 把随服务端发布的内置安装脚本写入缓存位置，返回是否已写入。
//
// 仅用于地址仍是内置默认值的条目：管理员改写过地址（如指向内网镜像）时，
// 内嵌版本不一定与其期望一致，不做兜底。
// 写入后把修改时间设为过期，后续正常请求/预热仍会优先尝试上游拿最新版本，
// 上游不可达时继续使用这份兜底。
func seedEmbeddedScript(target string, e extbin.Entry) bool {
	if !e.Script || !isDefaultEntry(e) {
		return false
	}
	data := examples.EmbeddedScript(e.Name)
	if len(data) == 0 {
		return false
	}
	unlock := lockExternalTarget(target)
	defer unlock()
	if fileExists(target) {
		return true // 并发请求已写入
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		log.Printf("⚠️ 写入内置脚本 %s 失败: %v", e.Name, err)
		return false
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".embedded-*")
	if err != nil {
		log.Printf("⚠️ 写入内置脚本 %s 失败: %v", e.Name, err)
		return false
	}
	tmpPath := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpPath)
		log.Printf("⚠️ 写入内置脚本 %s 失败: %v %v", e.Name, werr, cerr)
		return false
	}
	_ = os.Chmod(tmpPath, 0644)
	stale := time.Unix(0, 0)
	_ = os.Chtimes(tmpPath, stale, stale)
	_ = os.Remove(target)
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		log.Printf("⚠️ 写入内置脚本 %s 失败: %v", e.Name, err)
		return false
	}
	log.Printf("ℹ️ %s 上游不可用，已使用随服务端发布的内置版本", e.Name)
	return true
}

// isDefaultEntry 判断条目是否为内置条目且地址未被改写。
func isDefaultEntry(e extbin.Entry) bool {
	for name, v := range extbin.DefaultBinaries() {
		if !strings.EqualFold(name, e.Name) {
			continue
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(strings.ToLower(v), "script:") {
			v = strings.TrimSpace(v[len("script:"):])
		}
		return v == e.URL
	}
	return false
}
