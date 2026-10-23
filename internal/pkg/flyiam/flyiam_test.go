package flyiam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// fakeFlyIAM 提供用户分页与组织导出。
func fakeFlyIAM(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		write := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
		}
		switch r.URL.Path {
		case "/api/users/export":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			items := []map[string]any{}
			start := 0
			if page == 2 {
				start = 500
			}
			for i := start; i < start+500 && i < 501; i++ {
				items = append(items, map[string]any{
					"username": fmt.Sprintf("u%03d", i), "displayName": "用户",
					"properties": map[string]string{
						"deptIdLv0": "g", "deptNameLv0": "集团",
						"deptIdLv1": "rd", "deptNameLv1": "研发",
					},
				})
			}
			w.Header().Set("X-Total-Count", "501")
			write(items)
		case "/api/departments/export":
			write([]map[string]any{
				{"deptId": "g", "deptName": "集团", "level": 0, "fullPath": "集团", "userCount": 0},
				{"deptId": "rd", "deptName": "研发", "parentId": "g", "level": 1, "fullPath": "集团/研发", "userCount": 501},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFetchUsersPagination 分页拉取并去重，直到不足一页为止。
func TestFetchUsersPagination(t *testing.T) {
	srv := fakeFlyIAM(t)
	c := NewClient(srv.URL, "tok")
	if c == nil {
		t.Fatal("client 不应为 nil")
	}
	users, err := c.FetchUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 501 {
		t.Fatalf("应有 501 个用户, got %d", len(users))
	}
	if users[0].Username != "u000" || users[500].Username != "u500" {
		t.Fatalf("分页顺序不符: %q / %q", users[0].Username, users[500].Username)
	}
	if users[0].Properties["deptNameLv1"] != "研发" {
		t.Fatalf("属性未解析: %+v", users[0].Properties)
	}
}

// TestFetchDepartments 组织树解析。
func TestFetchDepartments(t *testing.T) {
	srv := fakeFlyIAM(t)
	c := NewClient(srv.URL, "tok")
	deps, err := c.FetchDepartments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 || deps[1].DeptID != "rd" || deps[1].ParentID != "g" ||
		deps[1].UserCount != 501 || deps[1].FullPath != "集团/研发" {
		t.Fatalf("组织解析不符: %+v", deps)
	}
}

// ===== 原有测试（凭据/字段/错误处理）=====

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

// TestFetchUsers 用户列表只读拉取。
func TestFetchUsers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/export" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Service-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":[
		  {"username":"alice","displayName":"爱丽丝","email":"a@x.com","properties":{"empCode":"E1"}},
		  {"username":"bob","displayName":"鲍勃"}
		]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	users, err := c.FetchUsers(context.Background())
	if err != nil {
		t.Fatalf("FetchUsers: %v", err)
	}
	if len(users) != 2 || users[0].Username != "alice" {
		t.Fatalf("用户不符: %+v", users)
	}
	if users[0].Properties["empCode"] != "E1" {
		t.Fatalf("扩展字段未解析: %+v", users[0].Properties)
	}
}
