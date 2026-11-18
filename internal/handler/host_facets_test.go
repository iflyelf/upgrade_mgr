package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
)

// TestHostFacetsFieldsParam ?fields= 显式指定字段（主机页下拉用），非法列名被白名单过滤，租户隔离。
func TestHostFacetsFieldsParam(t *testing.T) {
	db, sc := setupHostDB(t)
	tag := fmt.Sprintf("FCT-%d", time.Now().UnixNano())
	id, _ := insertTestHost(t, db, 1)
	if _, err := db.Exec(`UPDATE hosts SET app_code=$2, os_family='centos' WHERE id=$1`, id, tag); err != nil {
		t.Fatal(err)
	}

	call := func(query string, tenant int64) (fields []string, values map[string][]string) {
		req := httptest.NewRequest(http.MethodGet, "/api/hosts/facets"+query, nil)
		req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(tenant)))
		rec := httptest.NewRecorder()
		HostFacets(sc)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("应 200，实际 %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data struct {
				Fields []string            `json:"fields"`
				Values map[string][]string `json:"values"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Data.Fields, out.Data.Values
	}
	contains := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}

	// 未配置 task.host_selector_fields（sc.Settings 为空）时，显式 fields 仍生效；
	// 非白名单列名（ip、注入片段）被丢弃。
	// 重复字段去重；分号需编码（Go 会丢弃含裸分号的查询参数）。
	fields, values := call("?fields=app_code,os_family,ip,app_code,x%3BDROP", 1)
	if len(fields) != 2 || fields[0] != "app_code" || fields[1] != "os_family" {
		t.Fatalf("字段应只保留白名单: %v", fields)
	}
	if !contains(values["app_code"], tag) || !contains(values["os_family"], "centos") {
		t.Fatalf("应返回去重值: %v", values)
	}

	// 其他租户看不到本租户的值。
	_, values = call("?fields=app_code", 9906)
	if contains(values["app_code"], tag) {
		t.Fatalf("跨租户不应返回: %v", values["app_code"])
	}

	// 不传 fields 且未配置时返回空（任务页原有行为不变）。
	if fields, _ := call("", 1); len(fields) != 0 {
		t.Fatalf("未配置时应为空: %v", fields)
	}
}

// TestHostFacetsSearchAndLimit 主机页 app_code / 业务组候选值支持关键字搜索与条数限制。
//
// 候选值很多时前端按输入搜索，避免一次拉取全量；limit 不传时仍返回全部，
// 保持任务页选主机依赖的原有行为。
func TestHostFacetsSearchAndLimit(t *testing.T) {
	db, sc := setupHostDB(t)
	tag := fmt.Sprintf("FCS-%d", time.Now().UnixNano())
	vals := []string{tag + "-A", tag + "-B", tag + "-C"}
	for _, v := range vals {
		id, _ := insertTestHost(t, db, 1)
		if _, err := db.Exec(`UPDATE hosts SET app_code=$2, biz_group=$2 WHERE id=$1`, id, v); err != nil {
			t.Fatal(err)
		}
	}

	call := func(query string) (map[string][]string, map[string]bool) {
		req := httptest.NewRequest(http.MethodGet, "/api/hosts/facets"+query, nil)
		req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
		rec := httptest.NewRecorder()
		HostFacets(sc)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("应 200，实际 %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data struct {
				Values    map[string][]string `json:"values"`
				Truncated map[string]bool     `json:"truncated"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Data.Values, out.Data.Truncated
	}

	// 关键字命中单个值。
	values, _ := call("?fields=app_code&q=" + tag + "-A")
	if got := values["app_code"]; len(got) != 1 || got[0] != tag+"-A" {
		t.Fatalf("关键字搜索结果不符: %v", got)
	}

	// 同一搜索词 + limit=2：只返回 2 条并标记 truncated。
	values, truncated := call("?fields=app_code,biz_group&q=" + tag + "&limit=2")
	if len(values["app_code"]) != 2 || len(values["biz_group"]) != 2 {
		t.Fatalf("limit 未生效: %v", values)
	}
	if !truncated["app_code"] || !truncated["biz_group"] {
		t.Fatalf("应标记 truncated: %v", truncated)
	}

	// 不传 limit 时返回全部（原有行为）。
	values, truncated = call("?fields=app_code,biz_group&q=" + tag)
	if len(values["app_code"]) != len(vals) || len(values["biz_group"]) != len(vals) {
		t.Fatalf("未传 limit 应返回全部: %v", values)
	}
	if truncated["app_code"] || truncated["biz_group"] {
		t.Fatalf("未传 limit 不应标记 truncated: %v", truncated)
	}
}
