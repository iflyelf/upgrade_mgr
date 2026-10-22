package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/iflyelf/upgrade_mgr/web"
)

// spaHandler 提供内嵌前端静态文件，未命中文件回退 index.html（history 路由）。
func spaHandler() http.Handler {
	distFS, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		panic(err)
	}
	etags := buildETags(distFS)
	indexHTML := buildIndex(distFS, etags)
	if indexHTML != nil {
		// index.html 内容随资源 hash 变化，ETag 必须按改写后的内容计算，否则资源变了也会命中 304
		sum := sha256.Sum256(indexHTML)
		etags["index.html"] = `"` + hex.EncodeToString(sum[:16]) + `"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasPrefix(p, "api/") || p == "health" {
			http.NotFound(w, r)
			return
		}
		if p == "" {
			p = "index.html"
		}
		f, err := distFS.Open(p)
		if err != nil {
			p = "index.html"
			f, err = distFS.Open(p)
			if err != nil {
				http.NotFound(w, r)
				return
			}
		}
		defer f.Close()

		switch {
		case strings.HasSuffix(p, ".js"):
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		case strings.HasSuffix(p, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(p, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case strings.HasSuffix(p, ".svg"):
			w.Header().Set("Content-Type", "image/svg+xml")
		case strings.HasSuffix(p, ".ico"):
			w.Header().Set("Content-Type", "image/x-icon")
		case strings.HasSuffix(p, ".png"):
			w.Header().Set("Content-Type", "image/png")
		case strings.HasSuffix(p, ".webmanifest"):
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		// 资源文件名不带内容 hash，不能用 immutable 长缓存，否则升级二进制后浏览器仍使用旧前端；
		// 统一 no-cache + 内容 ETag，未变更时浏览器协商得到 304，开销很小。
		w.Header().Set("Cache-Control", "no-cache")
		if tag, ok := etags[p]; ok {
			w.Header().Set("ETag", tag)
		}
		if p == "index.html" && indexHTML != nil {
			http.ServeContent(w, r, p, time.Time{}, bytes.NewReader(indexHTML))
			return
		}
		if rs, ok := f.(io.ReadSeeker); ok {
			if st, e := f.Stat(); e == nil {
				http.ServeContent(w, r, p, st.ModTime(), rs)
				return
			}
		}
		_, _ = io.Copy(w, f)
	})
}

// buildETags 启动时按内容计算每个内嵌文件的强 ETag（embed.FS 的 ModTime 为零值，无法用 Last-Modified 协商）。
func buildETags(fsys fs.FS) map[string]string {
	tags := make(map[string]string)
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		tags[p] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})
	return tags
}

// buildIndex 给 index.html 中引用的 /assets/* 追加 ?v=<内容hash>，
// 使此前被 immutable 长缓存的旧资源 URL 失效，升级后浏览器必然拉取新文件。
func buildIndex(fsys fs.FS, etags map[string]string) []byte {
	b, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil
	}
	s := string(b)
	for p, tag := range etags {
		if !strings.HasPrefix(p, "assets/") {
			continue
		}
		v := strings.Trim(tag, `"`)[:8]
		for _, q := range []string{`"`, `'`} {
			s = strings.ReplaceAll(s, q+"/"+p+q, q+"/"+p+"?v="+v+q)
		}
	}
	return []byte(s)
}
