package extbin

import (
	"net/url"
	"strings"
	"testing"
)

func TestProxyURL(t *testing.T) {
	src := "https://github.com/a/b/releases/download/latest/x.exe"
	got, ok := ProxyURL(src, DefaultProxyBase, DefaultProxyHosts())
	if !ok {
		t.Fatal("GitHub 地址应走代理")
	}
	want := DefaultProxyBase + url.QueryEscape(src)
	if got != want {
		t.Fatalf("代理地址不符:\n got=%s\nwant=%s", got, want)
	}

	// raw 也走代理
	if _, ok := ProxyURL("https://raw.githubusercontent.com/a/b/main/x.ps1", DefaultProxyBase, DefaultProxyHosts()); !ok {
		t.Fatal("raw.githubusercontent.com 应走代理")
	}
	// 非代理域名直连
	if got, ok := ProxyURL("https://example.com/x.exe", DefaultProxyBase, DefaultProxyHosts()); ok || got != "https://example.com/x.exe" {
		t.Fatalf("非代理域名不应改写: got=%s ok=%v", got, ok)
	}
	// 已经指向代理，避免二次代理
	if got, ok := ProxyURL(DefaultProxyBase+"x", DefaultProxyBase, DefaultProxyHosts()); ok || got != DefaultProxyBase+"x" {
		t.Fatalf("代理自身不应二次代理: got=%s ok=%v", got, ok)
	}
	// 空代理表示直连
	if got, ok := ProxyURL(src, "", DefaultProxyHosts()); ok || got != src {
		t.Fatalf("空代理应直连: got=%s ok=%v", got, ok)
	}
	// 自定义模板与无 url= 的 base
	if got, ok := ProxyURL(src, "https://p.example/proxy?target={url}", DefaultProxyHosts()); !ok || !strings.Contains(got, "target=") {
		t.Fatalf("{url} 模板未替换: %s", got)
	}
	if got, ok := ProxyURL(src, "https://p.example/proxy", DefaultProxyHosts()); !ok || !strings.Contains(got, "?url=") {
		t.Fatalf("无 query 的代理地址拼接错误: %s", got)
	}
}
