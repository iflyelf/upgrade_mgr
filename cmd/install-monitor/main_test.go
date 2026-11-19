package main

import (
	"strings"
	"testing"
)

// TestTailText 失败日志只保留末尾，避免构建/安装输出过大。
func TestTailText(t *testing.T) {
	if got := tailText("abc", 10); got != "abc" {
		t.Fatalf("短文本不应截断: %q", got)
	}
	if got := tailText("0123456789", 4); got != "6789" {
		t.Fatalf("应保留末尾 4 字节: %q", got)
	}
	if got := tailText("中文日志", 4); len(got) != 4 || !strings.HasSuffix("中文日志", got) {
		t.Fatalf("按字节截断结果异常: %q", got)
	}
}

// TestMergeEnv 同名环境变量以本次传入值为准（Windows 大小写不敏感）。
func TestMergeEnv(t *testing.T) {
	got := mergeEnv([]string{"PATH=C:/x", "upgrade_mgr_external_url=http://old"}, "UPGRADE_MGR_EXTERNAL_URL=http://new")
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "http://old") || !strings.Contains(joined, "UPGRADE_MGR_EXTERNAL_URL=http://new") {
		t.Fatalf("旧环境变量未被覆盖: %v", got)
	}
	if !strings.Contains(joined, "PATH=C:/x") {
		t.Fatalf("其它环境变量应保留: %v", got)
	}
}
