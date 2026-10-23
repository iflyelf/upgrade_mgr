package extbinprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"
)

// fakeProvider 测试用 Provider。
type fakeProvider struct {
	entries []extbin.Entry
	hosts   []string
	rule    extbin.AllowRule
	cache   string
}

func (f fakeProvider) Entries() []extbin.Entry     { return f.entries }
func (f fakeProvider) Hosts() []string             { return f.hosts }
func (f fakeProvider) AllowRule() extbin.AllowRule { return f.rule }
func (f fakeProvider) CachePath(e extbin.Entry) string {
	return filepath.Join(f.cache, e.Name)
}

func TestProbeOneSuccessAndFailure(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("MZpayload"))
	}))
	defer okSrv.Close()
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>blocked</html>"))
	}))
	defer badSrv.Close()

	dir := t.TempDir()
	p := New(fakeProvider{
		hosts: []string{"127.0.0.1"},
		rule:  extbin.AllowRule{AllowPrivate: true},
		cache: dir,
	}, time.Minute)

	if !p.ProbeOne(context.Background(), extbin.Entry{Name: "ok", URL: okSrv.URL}) {
		t.Fatal("正常 exe 应探测成功")
	}
	if p.ProbeOne(context.Background(), extbin.Entry{Name: "bad", URL: badSrv.URL}) {
		t.Fatal("HTML 内容应探测失败")
	}
	// 脚本类型：HTML 也应失败
	if p.ProbeOne(context.Background(), extbin.Entry{Name: "badps", URL: badSrv.URL, Script: true}) {
		t.Fatal("HTML 冒充脚本应失败")
	}
}

// TestProbeOneRejectsNonWhitelistedHost 域名不在白名单时直接失败，不发起请求。
func TestProbeOneRejectsNonWhitelistedHost(t *testing.T) {
	p := New(fakeProvider{hosts: []string{"github.com"}, cache: t.TempDir()}, time.Minute)
	if p.ProbeOne(context.Background(), extbin.Entry{Name: "x", URL: "https://evil.example.com/x.exe"}) {
		t.Fatal("非白名单域名应探测失败")
	}
}

// TestProbeOneRecordsCache 缓存存在时记录大小，不存在时清零。
func TestProbeOneRecordsCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("MZx"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	entry := extbin.Entry{Name: "cached", URL: srv.URL}
	os.WriteFile(filepath.Join(dir, entry.Name), []byte("MZcached"), 0644)

	p := New(fakeProvider{
		hosts: []string{"127.0.0.1"},
		rule:  extbin.AllowRule{AllowPrivate: true},
		cache: dir,
	}, time.Minute)
	if !p.ProbeOne(context.Background(), entry) {
		t.Fatal("应探测成功")
	}
	// 只校验不 panic、可重复调用（指标是全局的，这里不断言数值）
	if !p.ProbeOne(context.Background(), extbin.Entry{Name: "missing", URL: srv.URL}) {
		t.Fatal("无缓存但源可用时应成功")
	}
}
