package web

import (
	"os"
	"strings"
	"testing"
)

// TestBackToTopButton 全局返回顶部按钮：滚动后出现、点击平滑回到顶部。
func TestBackToTopButton(t *testing.T) {
	js, err := os.ReadFile("dist/assets/app.js")
	if err != nil {
		t.Fatalf("读取 app.js 失败: %v", err)
	}
	for _, want := range []string{
		"function initBackToTop()", "class: 'back-to-top'",
		"window.scrollTo({ top: 0, behavior: 'smooth' })",
		"backTopBtn.classList.toggle('visible'",
		"prefers-reduced-motion", "aria-label': '返回顶部'",
	} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js 缺少返回顶部关键逻辑: %q", want)
		}
	}
	css, err := os.ReadFile("dist/assets/app.css")
	if err != nil {
		t.Fatalf("读取 app.css 失败: %v", err)
	}
	for _, want := range []string{
		".back-to-top {", "position: fixed", ".back-to-top.visible",
		"z-index: 250", "prefers-reduced-motion",
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("app.css 缺少返回顶部样式: %q", want)
		}
	}
}
