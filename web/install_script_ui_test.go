package web

import (
	"os"
	"strings"
	"testing"
)

// TestInstallScriptAutoLoads 接入脚本页必须自动加载脚本信息与命令，
// 不要求用户先点「生成接入脚本」；按钮只用于主动轮换令牌。
func TestInstallScriptAutoLoads(t *testing.T) {
	src, err := os.ReadFile("dist/assets/app.js")
	if err != nil {
		t.Fatalf("读取 app.js 失败: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"installScriptsTried",
		"/api/agent/install-monitor/token?format=json",
		"令牌在首次执行时自动创建",
		"16666/metrics",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("app.js 缺少接入脚本自动加载/自检提示: %q", want)
		}
	}
}
