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

// TestNewClientDisabled 未配置地址或令牌时返回 nil，避免误调用。
func TestNewClientDisabled(t *testing.T) {
	if NewClient("", "tok") != nil || NewClient("http://x", "") != nil {
		t.Fatal("缺少地址或令牌时应返回 nil")
	}
}
