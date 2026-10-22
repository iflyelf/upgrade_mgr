package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestSPAHandlerCacheBusting(t *testing.T) {
	h := spaHandler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !regexp.MustCompile(`/assets/app\.js\?v=[0-9a-f]{8}`).Match(body) {
		t.Fatalf("index.html 未给 app.js 追加版本参数: %s", body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("index Cache-Control = %q", cc)
	}
	indexTag := rec.Header().Get("ETag")
	if indexTag == "" {
		t.Fatal("index 缺少 ETag")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js?v=whatever", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("app.js status = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("app.js Cache-Control = %q，不应为 immutable 长缓存", cc)
	}
	jsTag := rec.Header().Get("ETag")
	if jsTag == "" {
		t.Fatal("app.js 缺少 ETag")
	}

	// 协商缓存：带相同 ETag 应返回 304
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Header.Set("If-None-Match", jsTag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match 命中应返回 304，got %d", rec.Code)
	}

	// history 路由回退 index.html，同样带版本参数
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/components", nil))
	body, _ = io.ReadAll(rec.Body)
	if rec.Header().Get("ETag") != indexTag || !regexp.MustCompile(`\?v=`).Match(body) {
		t.Fatal("history 路由回退的 index.html 未改写")
	}

	// favicon.ico 必须直接返回图标，不能被 SPA 回退成 index.html，
	// 且 Content-Type 要正确，否则浏览器地址栏不显示站点图标。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("favicon status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/x-icon" {
		t.Fatalf("favicon Content-Type = %q", ct)
	}
	if rec.Body.Len() < 100 {
		t.Fatalf("favicon 内容异常，长度 %d", rec.Body.Len())
	}
}
