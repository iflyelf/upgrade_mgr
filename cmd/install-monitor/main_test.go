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
