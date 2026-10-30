package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if out["result"].(map[string]any)["protocolVersion"] != SupportedProtocolVersions[0] {
		t.Fatal("不支持的版本应回落为最新版本")
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
