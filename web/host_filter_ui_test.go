package web

import (
	"os"
	"strings"
	"testing"
)

// TestHostFilterSearchableCombo 主机页筛选框必须支持输入搜索，并包含业务组。
//
// app_code 候选值可能非常多：下拉必须按需向 /api/hosts/facets 搜索（带 q/limit），
// 业务组紧跟在 app_code 后面，且筛选参数会进入列表/导出/按筛选全选。
func TestHostFilterSearchableCombo(t *testing.T) {
	src, err := os.ReadFile("dist/assets/app.js")
	if err != nil {
		t.Fatalf("读取 app.js 失败: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"function tagFilter(opts)",
		"hostFilterTag('app_code'",
		"hostFilterTag('biz_group'",
		"hostFilterTag('os_family'",
		"hostFilterTag('account'",
		"hostFilterTag('agent_status'",
		"hostFilterTag('upgrade_status'",
		"addAll('biz_group'",
		"asList(p.biz_group)",
		"/api/hosts/facets?fields=",
		"&q=",
		"&limit=200",
		"tag-filter-box",
		"tag-label",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("app.js 缺少主机筛选搜索/业务组关键逻辑: %q", want)
		}
	}
	css, err := os.ReadFile("dist/assets/app.css")
	if err != nil {
		t.Fatalf("读取 app.css 失败: %v", err)
	}
	for _, want := range []string{".tag-label", "flex: 0 0 auto"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("app.css 缺少长标签截断/删除按钮样式: %q", want)
		}
	}
	// 筛选框只保留多选标签（选一个即单选），不再有额外的「单选」复选框。
	for _, banned := range []string{"tag-filter-mode", "hostFilterModes", "筛选单选模式"} {
		if strings.Contains(body, banned) {
			t.Errorf("app.js 不应再包含单选开关逻辑: %q", banned)
		}
	}
}

// TestTaskHostFilterUsesTagFilter 任务页主机筛选必须与主机页共用同一个可搜索标签框。
func TestTaskHostFilterUsesTagFilter(t *testing.T) {
	src, err := os.ReadFile("dist/assets/app.js")
	if err != nil {
		t.Fatalf("读取 app.js 失败: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "var picker = tagFilter({") {
		t.Error("任务页主机筛选未复用 tagFilter")
	}
	for _, banned := range []string{"filterModes", "筛选单选模式"} {
		if strings.Contains(body, banned) {
			t.Errorf("任务页不应再包含单选模式逻辑: %q", banned)
		}
	}
}
