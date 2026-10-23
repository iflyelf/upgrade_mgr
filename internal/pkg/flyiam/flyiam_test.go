package flyiam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchAppCredentials 正常返回凭据；错误码/HTTP 错误应失败。
func TestFetchAppCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/casdoor/app-credentials":
			_, _ = w.Write([]byte(`{"code":0,"data":{"endpoint":"http://casdoor","organization":"org",
			  "application":"app","clientId":"cid","clientSecret":"sec"}}`))
		case "/api/user-fields/export":
			_, _ = w.Write([]byte(`{"code":0,"data":[
			  {"fieldKey":"empCode","label":"工号","fieldType":"text","showInList":true,"showInForm":true,"editable":true,"sortOrder":10},
			  {"fieldKey":"compileType","label":"编制类型","fieldType":"select","options":"[\"正编\",\"外包\"]","sortOrder":20}
			]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	if c == nil {
		t.Fatal("客户端应为非空")
	}
	creds, err := c.FetchAppCredentials(context.Background())
	if err != nil {
		t.Fatalf("FetchAppCredentials: %v", err)
	}
	if creds.ClientID != "cid" || creds.ClientSecret != "sec" {
		t.Fatalf("凭据不符: %+v", creds)
	}

	defs, err := c.FetchUserFields(context.Background())
	if err != nil {
		t.Fatalf("FetchUserFields: %v", err)
	}
	if len(defs) != 2 || defs[0].FieldKey != "empCode" {
		t.Fatalf("字段定义不符: %+v", defs)
	}
}

// TestNotConfigured 未配置时返回 nil 客户端，调用报错。
func TestNotConfigured(t *testing.T) {
	if NewClient("", "tok") != nil || NewClient("http://x", "") != nil {
		t.Fatal("缺地址或令牌应返回 nil")
	}
	var c *Client
	if _, err := c.FetchUserFields(context.Background()); err == nil {
		t.Fatal("nil 客户端应报错")
	}
}

// TestErrorEnvelope 业务错误码应透出 message。
func TestErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":403,"message":"token invalid"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad")
	if _, err := c.FetchAppCredentials(context.Background()); err == nil {
		t.Fatal("业务错误码应返回 error")
	}
}

// TestHTMLResponse HTML 页面（地址填错）应给出明确错误。
func TestHTMLResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html>login</html>`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, err := c.FetchAppCredentials(context.Background())
	if err == nil {
		t.Fatal("HTML 响应应报错")
	}
	t.Logf("err: %v", err)
}

// TestFieldJSONShape FlyIAM 字段定义的 JSON 键与本地结构一致（防止协议漂移）。
func TestFieldJSONShape(t *testing.T) {
	raw := `{"fieldKey":"k","label":"L","fieldType":"text","options":"[]",
	         "showInList":true,"showInForm":false,"editable":true,"builtin":false,"sortOrder":5}`
	var d UserFieldDef
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	if d.FieldKey != "k" || !d.ShowInList || d.ShowInForm || d.SortOrder != 5 {
		t.Fatalf("解析不符: %+v", d)
	}
}
