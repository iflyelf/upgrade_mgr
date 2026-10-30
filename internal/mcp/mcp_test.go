package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeProvider struct{}

func (fakeProvider) Info(context.Context) Info {
	return Info{Name: "test-srv", Version: "1.0", Instructions: "hello"}
}

func (fakeProvider) ListTools(context.Context) []ToolInfo {
	return []ToolInfo{{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}}
}

func (fakeProvider) CallTool(_ context.Context, name string, args json.RawMessage) (*CallResult, error) {
	if name != "echo" {
		return nil, ErrUnknownTool
	}
	return TextResult(string(args), false), nil
}

func post(t *testing.T, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	s := &Server{Provider: fakeProvider{}}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)))
	var out map[string]any
	if rec.Code == http.StatusOK && strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "{") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是 JSON: %v %s", err, rec.Body.String())
		}
	}
	return rec, out
}

func TestInitializeNegotiatesVersion(t *testing.T) {
	_, out := post(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" {
		t.Fatalf("应回显受支持的客户端版本: %v", res["protocolVersion"])
	}
	if res["instructions"] != "hello" {
		t.Fatalf("instructions 丢失: %v", res)
	}
	_, out = post(t, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	// 旧握手不支持的版本回落为最新旧版本；现代版本无握手，不出现在 initialize 结果中。
	if got := out["result"].(map[string]any)["protocolVersion"]; got != "2025-11-25" {
		t.Fatalf("不支持的版本应回落为最新旧版本 2025-11-25: %v", got)
	}
}

func TestNotificationAccepted(t *testing.T) {
	rec, _ := post(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Fatalf("通知应返回 202 无响应体: %d %q", rec.Code, rec.Body.String())
	}
}

func TestToolsListAndCall(t *testing.T) {
	_, out := post(t, `{"jsonrpc":"2.0","id":"a","method":"tools/list"}`)
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools=%v", tools)
	}
	_, out = post(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"x":1}}}`)
	res := out["result"].(map[string]any)
	if res["isError"] != false || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), `"x":1`) {
		t.Fatalf("调用结果不符: %v", res)
	}
	_, out = post(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`)
	if int(out["error"].(map[string]any)["code"].(float64)) != CodeInvalidParams {
		t.Fatalf("未知工具应返回 -32602: %v", out)
	}
}

func TestErrors(t *testing.T) {
	rec, _ := post(t, `{bad json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400: %d", rec.Code)
	}
	_, out := post(t, `{"jsonrpc":"2.0","id":5,"method":"no/such"}`)
	if int(out["error"].(map[string]any)["code"].(float64)) != CodeMethodNotFound {
		t.Fatalf("未知方法应 -32601: %v", out)
	}
	s := &Server{Provider: fakeProvider{}}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405: %d", rec.Code)
	}
}

func TestBatch(t *testing.T) {
	s := &Server{Provider: fakeProvider{}}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"}]`)))
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 1 {
		t.Fatalf("批量应只返回请求的响应: %v %s", err, rec.Body.String())
	}
}

const modernVer = "2026-07-28"

func postModern(t *testing.T, hdr map[string]string, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	s := &Server{Provider: modernProvider{}}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var out map[string]any
	if strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "{") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析响应失败: %v %s", err, rec.Body.String())
		}
	}
	return rec, out
}

type modernProvider struct{ fakeProvider }

func (modernProvider) Info(context.Context) Info {
	return Info{Name: "test-srv", Version: "1.0", Instructions: "hello", ListCacheTTL: 5 * time.Minute}
}

func metaBody(id int, method, extra string) string {
	params := `"_meta":{"` + MetaProtocolVersion + `":"` + modernVer + `"}`
	if extra != "" {
		params = extra + "," + params
	}
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"` + method + `","params":{` + params + `}}`
}

func errCode(out map[string]any) float64 {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(float64)
	return c
}

func TestModernDiscover(t *testing.T) {
	rec, out := postModern(t, map[string]string{HeaderProtocolVersion: modernVer, HeaderMethod: "server/discover"},
		metaBody(1, "server/discover", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	res := out["result"].(map[string]any)
	vers := res["supportedVersions"].([]any)
	if vers[0] != modernVer {
		t.Fatalf("supportedVersions 首项应为 %s: %v", modernVer, vers)
	}
	if res["resultType"] != "complete" || res["instructions"] != "hello" {
		t.Fatalf("discover 结果不完整: %v", res)
	}
	si := res["_meta"].(map[string]any)[MetaServerInfo].(map[string]any)
	if si["name"] != "test-srv" {
		t.Fatalf("serverInfo 错误: %v", si)
	}
}

func TestModernToolsListCacheHints(t *testing.T) {
	_, out := postModern(t, map[string]string{HeaderProtocolVersion: modernVer, HeaderMethod: "tools/list"},
		metaBody(1, "tools/list", ""))
	res := out["result"].(map[string]any)
	if res["cacheScope"] != "private" || res["ttlMs"] != float64(300000) || res["resultType"] != "complete" {
		t.Fatalf("缓存提示缺失: %v", res)
	}
}

func TestModernToolsCallWithBase64Name(t *testing.T) {
	hdr := map[string]string{HeaderProtocolVersion: modernVer, HeaderMethod: "tools/call", HeaderName: EncodeHeaderValue("echo")}
	_, out := postModern(t, hdr, metaBody(1, "tools/call", `"name":"echo","arguments":{"a":1}`))
	res := out["result"].(map[string]any)
	if res["isError"] != false || res["resultType"] != "complete" {
		t.Fatalf("tools/call 失败: %v", out)
	}
	// 哨兵编码同样应被接受
	if v := EncodeHeaderValue("中文"); !strings.HasPrefix(v, "=?base64?") {
		t.Fatalf("非 ASCII 应 base64 编码: %s", v)
	}
	hdr[HeaderName] = "=?base64?ZWNobw==?="
	if _, out = postModern(t, hdr, metaBody(2, "tools/call", `"name":"echo"`)); out["error"] != nil {
		t.Fatalf("base64 哨兵 Mcp-Name 应被接受: %v", out)
	}
}

func TestModernHeaderMismatch(t *testing.T) {
	cases := []struct {
		name string
		hdr  map[string]string
		body string
	}{
		{"缺少 Mcp-Method", map[string]string{HeaderProtocolVersion: modernVer}, metaBody(1, "tools/list", "")},
		{"Mcp-Method 不一致", map[string]string{HeaderProtocolVersion: modernVer, HeaderMethod: "ping"}, metaBody(1, "tools/list", "")},
		{"Mcp-Name 不一致", map[string]string{HeaderProtocolVersion: modernVer, HeaderMethod: "tools/call", HeaderName: "other"},
			metaBody(1, "tools/call", `"name":"echo"`)},
		{"缺少 Mcp-Name", map[string]string{HeaderProtocolVersion: modernVer, HeaderMethod: "tools/call"},
			metaBody(1, "tools/call", `"name":"echo"`)},
		{"版本头与 _meta 不一致", map[string]string{HeaderProtocolVersion: "2025-11-25", HeaderMethod: "tools/list"}, metaBody(1, "tools/list", "")},
		{"现代头缺少 _meta 版本", map[string]string{HeaderProtocolVersion: modernVer, HeaderMethod: "tools/list"},
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
	}
	for _, c := range cases {
		rec, out := postModern(t, c.hdr, c.body)
		if rec.Code != http.StatusBadRequest || errCode(out) != CodeHeaderMismatch {
			t.Errorf("%s: 期望 400/-32020，得到 %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
}

func TestModernUnsupportedVersion(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"` + MetaProtocolVersion + `":"2099-01-01"}}}`
	rec, out := postModern(t, map[string]string{HeaderProtocolVersion: "2099-01-01", HeaderMethod: "tools/list"}, body)
	if rec.Code != http.StatusBadRequest || errCode(out) != CodeUnsupportedVersion {
		t.Fatalf("期望 400/-32022，得到 %d %s", rec.Code, rec.Body.String())
	}
	data := out["error"].(map[string]any)["data"].(map[string]any)
	if sup := data["supported"].([]any); len(sup) != len(SupportedProtocolVersions) {
		t.Fatalf("data.supported 错误: %v", data)
	}
}

func TestModernUnknownMethod404(t *testing.T) {
	rec, out := postModern(t, map[string]string{HeaderProtocolVersion: modernVer, HeaderMethod: "foo/bar"},
		metaBody(1, "foo/bar", ""))
	if rec.Code != http.StatusNotFound || errCode(out) != CodeMethodNotFound {
		t.Fatalf("期望 404/-32601，得到 %d %s", rec.Code, rec.Body.String())
	}
}

func TestModernRejectsBatch(t *testing.T) {
	rec, out := postModern(t, map[string]string{HeaderProtocolVersion: modernVer, HeaderMethod: "tools/list"},
		`[`+metaBody(1, "tools/list", "")+`]`)
	if rec.Code != http.StatusBadRequest || errCode(out) != CodeInvalidRequest {
		t.Fatalf("现代版本应拒绝批量: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLegacyHeaderlessStillWorks(t *testing.T) {
	// 无版本头、无 _meta：视为 2025-03-26，旧客户端照常使用，结果不带现代缓存字段。
	rec, out := postModern(t, nil, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if _, has := out["result"].(map[string]any)["cacheScope"]; has {
		t.Fatalf("旧版本不应返回 cacheScope: %v", out)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	s := &Server{Provider: fakeProvider{}}
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(m, "/mcp", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 应返回 405: %d", m, rec.Code)
		}
	}
}
