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
		"function filterTags(key, label, options, remoteField)",
		"filterTags('app_code', 'app_code'",
		"filterTags('biz_group', '业务组'",
		"hostFilterModes",
		"' 单选'",
		"addAll('biz_group'",
		"asList(p.biz_group)",
		"/api/hosts/facets?fields=",
		"&q=",
		"&limit=200",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("app.js 缺少主机筛选搜索/业务组关键逻辑: %q", want)
		}
	}
}
