package casdoor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestDirectoryList(t *testing.T) {
	var gotAuth bool
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/get-users" {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		u, p, ok := r.BasicAuth()
		gotAuth = ok && u == "cid" && p == "csecret"
		gotQuery = r.URL.Query()
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"data": []map[string]any{
				{"name": "alice", "displayName": "Alice", "email": "alice@example.com",
					"owner": "flyiam", "properties": map[string]string{"empCode": "001"}},
			},
			"data2": 29871,
		})
	}))
	defer srv.Close()

	d := NewDirectory(srv.URL, "cid", "csecret", "flyiam")
	list, total, err := d.List(context.Background(), 2, 20, "displayName", "王")
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if !gotAuth {
		t.Fatal("必须使用 client_id:client_secret 做 Basic 认证")
	}
	if gotQuery.Get("owner") != "flyiam" || gotQuery.Get("p") != "2" ||
		gotQuery.Get("pageSize") != "20" || gotQuery.Get("field") != "displayName" ||
		gotQuery.Get("value") != "王" || gotQuery.Get("sortField") != "name" {
		t.Fatalf("查询参数不正确: %v", gotQuery)
	}
	if total != 29871 {
		t.Fatalf("total = %d，期望 29871", total)
	}
	if len(list) != 1 || list[0].Name != "alice" || list[0].Properties["empCode"] != "001" {
		t.Fatalf("用户解析错误: %+v", list)
	}
}

func TestDirectoryGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/get-user" {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		if r.URL.Query().Get("id") != "flyiam/bob" {
			t.Errorf("id 参数错误: %s", r.URL.Query().Get("id"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"data":   map[string]any{"name": "bob", "displayName": "Bob", "owner": "flyiam"},
		})
	}))
	defer srv.Close()

	d := NewDirectory(srv.URL, "cid", "csecret", "flyiam")
	u, err := d.Get(context.Background(), "bob")
	if err != nil || u == nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if u.Name != "bob" || u.DisplayName != "Bob" {
		t.Fatalf("用户解析错误: %+v", u)
	}

	// 不存在返回 nil, nil
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": nil})
	}))
	defer srv2.Close()
	d2 := NewDirectory(srv2.URL, "cid", "csecret", "flyiam")
	u2, err := d2.Get(context.Background(), "nobody")
	if err != nil || u2 != nil {
		t.Fatalf("不存在的用户应返回 nil, nil；实际 %+v, %v", u2, err)
	}
}
