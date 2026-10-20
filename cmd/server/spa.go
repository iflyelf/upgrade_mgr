package main

import (
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/iflyelf/upgrade_mgr/web"
)

// spaHandler 提供内嵌前端静态文件，未命中文件回退 index.html（history 路由）。
func spaHandler() http.Handler {
	distFS, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		panic(err)
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
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
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
