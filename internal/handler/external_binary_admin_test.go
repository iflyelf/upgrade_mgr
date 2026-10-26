package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/extbin"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/setting"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// newExtbinContext 构造带设置服务的内存上下文（配置用 config，设置落 DB）。
func newExtbinContext(t *testing.T, dir string) (*svc.ServiceContext, *config.Config) {
	t.Helper()
	db, sc := setupHostDB(t)
	cfg := config.Default()
	cfg.Server.AgentBinaryDir = dir
	cfg.Server.ExternalBinaries = map[string]string{
		"custom-tool": "https://github.com/example/custom-tool.exe",
	}
	cfg.Server.ExternalBinaryTTL = "24h"
	sc.Config = cfg
	sc.Settings = setting.NewService(db, cfg)
	return sc, cfg
}

// TestListExternalBinariesIncludesCacheState 列表返回白名单与缓存状态。
func TestListExternalBinariesIncludesCacheState(t *testing.T) {
	dir := t.TempDir()
	sc, _ := newExtbinContext(t, dir)
	// 预置缓存文件
	cache := filepath.Join(dir, ".external-cache")
	os.MkdirAll(cache, 0755)
	os.WriteFile(filepath.Join(cache, "custom-tool.exe"), []byte("MZxx"), 0644)

	req := httptest.NewRequest("GET", "/api/external/binaries", nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	ListExternalBinaries(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data externalBinaryListResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if len(out.Data.Items) != 1 || out.Data.Items[0].Name != "custom-tool" {
		t.Fatalf("条目不符: %+v", out.Data.Items)
	}
	if !out.Data.Items[0].Cached || out.Data.Items[0].CachedSize != 4 {
		t.Fatalf("缓存状态不符: %+v", out.Data.Items[0])
	}
	if out.Data.Items[0].PlatformURL != "/api/external/binary/custom-tool" {
		t.Fatalf("平台地址不符: %q", out.Data.Items[0].PlatformURL)
	}
	if out.Data.Source != "config" {
		t.Fatalf("source 应为 config: %q", out.Data.Source)
	}
}

// TestSaveExternalBinariesHotReload 保存后立即生效（无需重启）。
func TestSaveExternalBinariesHotReload(t *testing.T) {
	dir := t.TempDir()
	sc, cfg := newExtbinContext(t, dir)

	body := `{"items":[{"name":"new-tool","url":"https://github.com/example/new-tool.exe","script":false},
	                    {"name":"new-install","url":"https://raw.githubusercontent.com/example/i.ps1","script":true}],
	          "hosts":["github.com","raw.githubusercontent.com"],
	          "ttl":"12h","allow_private":false,"allow_cidrs":["10.0.0.0/8"]}`
	req := httptest.NewRequest("PUT", "/api/external/binaries", strings.NewReader(body))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	SaveExternalBinaries(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存失败 %d: %s", rec.Code, rec.Body.String())
	}

	// 内存配置应立即反映
	snap := sc.Settings.Snapshot()
	if len(snap.Server.ExternalBinaries) != 2 {
		t.Fatalf("内存配置未更新: %v", snap.Server.ExternalBinaries)
	}
	if !strings.HasPrefix(snap.Server.ExternalBinaries["new-install"], "script:") {
		t.Fatalf("script 前缀未保留: %v", snap.Server.ExternalBinaries)
	}
	if snap.Server.ExternalBinaryTTL != "12h" {
		t.Fatalf("ttl 未更新: %q", snap.Server.ExternalBinaryTTL)
	}
	if len(snap.Server.ExternalBinaryAllowCIDRs) != 1 {
		t.Fatalf("放行网段未更新: %v", snap.Server.ExternalBinaryAllowCIDRs)
	}
	_ = cfg
}

// TestSaveExternalBinariesRejectsBadInput 非法输入应 400，且不落库。
func TestSaveExternalBinariesRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	sc, _ := newExtbinContext(t, dir)

	cases := []string{
		`{"items":[{"name":"a/b","url":"https://github.com/x"}]}`,                                             // 非法名称
		`{"items":[{"name":"ok","url":"ftp://github.com/x"}]}`,                                                // 非法协议
		`{"items":[{"name":"ok","url":"https://evil.example.com/x"}]}`,                                        // 域名不在白名单
		`{"items":[{"name":"dup","url":"https://github.com/x"},{"name":"dup","url":"https://github.com/y"}]}`, // 重名
		`{"items":[{"name":"ok","url":"https://github.com/x"}],"ttl":"abc"}`,                                  // 非法 TTL
		`{"items":[{"name":"ok","url":"https://github.com/x"}],"allow_cidrs":["10.0.0.0/33"]}`,                // 非法网段
	}
	for i, body := range cases {
		req := httptest.NewRequest("PUT", "/api/external/binaries", strings.NewReader(body))
		req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
		rec := httptest.NewRecorder()
		SaveExternalBinaries(sc)(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("第 %d 个用例应 400，实际 %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
}

// TestTestExternalBinaryHitsCache 命中缓存时快速返回，不访问上游。
func TestTestExternalBinaryHitsCache(t *testing.T) {
	dir := t.TempDir()
	sc, _ := newExtbinContext(t, dir)
	cache := filepath.Join(dir, ".external-cache")
	os.MkdirAll(cache, 0755)
	os.WriteFile(filepath.Join(cache, "custom-tool.exe"), []byte("MZpayload"), 0644)

	req := httptest.NewRequest("POST", "/api/external/binaries/test",
		strings.NewReader(`{"name":"custom-tool"}`))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	TestExternalBinary(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("测试失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data externalBinaryTestResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if !out.Data.OK || !out.Data.Cached {
		t.Fatalf("应命中缓存: %+v", out.Data)
	}
	if out.Data.Size != 9 {
		t.Fatalf("size=%d", out.Data.Size)
	}
}

// TestTestExternalBinaryRejectsInternal 内网地址默认拒绝，提示放行方式。
func TestTestExternalBinaryRejectsInternal(t *testing.T) {
	dir := t.TempDir()
	sc, cfg := newExtbinContext(t, dir)
	// 先把 127.0.0.1 加入域名白名单，才能走到内网地址策略判断。
	cfg.Server.ExternalBinaryHosts = []string{"127.0.0.1"}

	req := httptest.NewRequest("POST", "/api/external/binaries/test",
		strings.NewReader(`{"name":"candidate","url":"http://127.0.0.1:9/x.exe"}`))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	TestExternalBinary(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("测试接口应返回 200 + ok=false，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data externalBinaryTestResult `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Data.OK {
		t.Fatal("内网地址不应通过")
	}
	if !strings.Contains(out.Data.Hint, "放行") {
		t.Fatalf("应提示如何放行: %q", out.Data.Hint)
	}
}

// TestRefreshExternalBinary 强制重下：删除旧缓存并重新落盘。
func TestRefreshExternalBinary(t *testing.T) {
	dir := t.TempDir()
	sc, cfg := newExtbinContext(t, dir)

	// 用本地 httptest 作为上游，并把它加进域名白名单（127.0.0.1 需要内网放行）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("MZfresh-binary"))
	}))
	defer srv.Close()
	cfg.Server.ExternalBinaries = map[string]string{"local-tool": srv.URL + "/tool.exe"}
	cfg.Server.ExternalBinaryHosts = []string{"127.0.0.1"}
	cfg.Server.ExternalBinaryAllowPrivate = true

	// 预置旧缓存
	cache := filepath.Join(dir, ".external-cache")
	os.MkdirAll(cache, 0755)
	oldPath := filepath.Join(cache, "local-tool.exe")
	os.WriteFile(oldPath, []byte("MZold"), 0644)

	req := httptest.NewRequest("POST", "/api/external/binaries/refresh",
		strings.NewReader(`{"name":"local-tool"}`))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	RefreshExternalBinary(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("刷新失败 %d: %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(oldPath)
	if err != nil || string(got) != "MZfresh-binary" {
		t.Fatalf("缓存未更新: %q err=%v", string(got), err)
	}

	// 不存在的名称 → 404
	req = httptest.NewRequest("POST", "/api/external/binaries/refresh",
		strings.NewReader(`{"name":"missing"}`))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec = httptest.NewRecorder()
	RefreshExternalBinary(sc)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在名称应 404，实际 %d", rec.Code)
	}
}

// TestRefreshRejectsInternalWithoutAllow 未放行内网时，刷新应失败且不写缓存。
func TestRefreshRejectsInternalWithoutAllow(t *testing.T) {
	dir := t.TempDir()
	sc, cfg := newExtbinContext(t, dir)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("MZx"))
	}))
	defer srv.Close()
	cfg.Server.ExternalBinaries = map[string]string{"local-tool": srv.URL + "/t.exe"}
	cfg.Server.ExternalBinaryHosts = []string{"127.0.0.1"}
	cfg.Server.ExternalBinaryAllowPrivate = false

	req := httptest.NewRequest("POST", "/api/external/binaries/refresh",
		strings.NewReader(`{"name":"local-tool"}`))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	RefreshExternalBinary(sc)(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("应拒绝内网上游，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".external-cache", "local-tool.exe")); err == nil {
		t.Fatal("失败时不应留下缓存文件")
	}
}

// TestProbeElapsed 耗时必须在返回值里（命名返回值 + defer 的经典坑）。
func TestProbeElapsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte("MZpayload"))
	}))
	defer srv.Close()

	res := probeExternalBinary(context.Background(),
		extbin.Entry{Name: "x", URL: srv.URL}, []string{"127.0.0.1"},
		extbin.AllowRule{AllowPrivate: true}, "", nil)
	if !res.OK {
		t.Fatalf("应探测成功: %+v", res)
	}
	if res.ElapsedMS < 15 {
		t.Fatalf("耗时未写回返回值: %dms", res.ElapsedMS)
	}
	if res.StatusCode != http.StatusOK || res.Size == 0 {
		t.Fatalf("状态/大小不符: %+v", res)
	}
}

// TestListExternalBinariesPagination 服务端分页 + 搜索。
func TestListExternalBinariesPagination(t *testing.T) {
	dir := t.TempDir()
	sc, cfg := newExtbinContext(t, dir)
	items := map[string]string{}
	for _, n := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		items[n] = "https://github.com/example/" + n + ".exe"
	}
	cfg.Server.ExternalBinaries = items

	call := func(query string) externalBinaryListResponse {
		req := httptest.NewRequest("GET", "/api/external/binaries?"+query, nil)
		req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
		rec := httptest.NewRecorder()
		ListExternalBinaries(sc)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("查询失败 %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data externalBinaryListResponse `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析: %v", err)
		}
		return out.Data
	}

	// 第 1 页 size=2：total=5, pages=3
	page1 := call("page=1&size=2")
	if page1.Total != 5 || page1.Pages != 3 || len(page1.Items) != 2 {
		t.Fatalf("分页错误: total=%d pages=%d items=%d", page1.Total, page1.Pages, len(page1.Items))
	}
	// 名称排序：alpha/beta
	if page1.Items[0].Name != "alpha" || page1.Items[1].Name != "beta" {
		t.Fatalf("排序不符: %+v", page1.Items)
	}
	// 越界页夹紧到最后一页
	last := call("page=99&size=2")
	if last.Page != 3 || len(last.Items) != 1 {
		t.Fatalf("越界页应夹紧: page=%d items=%d", last.Page, len(last.Items))
	}
	// 搜索：匹配 gamma
	searched := call("q=gam")
	if searched.Total != 1 || searched.Items[0].Name != "gamma" {
		t.Fatalf("搜索错误: %+v", searched.Items)
	}
	// 不传分页参数 → 返回全部（兼容旧行为）
	all := call("")
	if len(all.Items) != 5 || all.Page != 0 {
		t.Fatalf("未分页应返回全部: items=%d page=%d", len(all.Items), all.Page)
	}
}

// TestSaveExternalBinariesMerge 分页场景下保存不能清空其它页。
func TestSaveExternalBinariesMerge(t *testing.T) {
	dir := t.TempDir()
	sc, cfg := newExtbinContext(t, dir)
	cfg.Server.ExternalBinaries = map[string]string{
		"keep-a":  "https://github.com/example/a.exe",
		"keep-b":  "https://github.com/example/b.exe",
		"drop-me": "https://github.com/example/c.exe",
	}
	// 保存当前页（只含 keep-a 修改）+ 删除 drop-me
	body := `{"merge":true,
	  "items":[{"name":"keep-a","url":"https://github.com/example/a2.exe","script":false}],
	  "deleted":["drop-me"],
	  "hosts":["github.com"],"ttl":"24h"}`
	req := httptest.NewRequest("PUT", "/api/external/binaries", strings.NewReader(body))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	SaveExternalBinaries(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存失败 %d: %s", rec.Code, rec.Body.String())
	}
	snap := sc.Settings.Snapshot()
	if len(snap.Server.ExternalBinaries) != 2 {
		t.Fatalf("应保留 2 条（keep-a/keep-b），实际 %v", snap.Server.ExternalBinaries)
	}
	if _, ok := snap.Server.ExternalBinaries["keep-b"]; !ok {
		t.Fatal("其它页条目被清空（merge 未生效）")
	}
	if _, ok := snap.Server.ExternalBinaries["drop-me"]; ok {
		t.Fatal("deleted 未生效")
	}
	if snap.Server.ExternalBinaries["keep-a"] != "https://github.com/example/a2.exe" {
		t.Fatalf("upsert 未生效: %q", snap.Server.ExternalBinaries["keep-a"])
	}
}
