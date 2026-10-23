package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateScriptContent 空文件与 HTML 错误页必须提前拒绝，
// 否则解释器会“成功”执行空内容，任务随后卡在服务未注册/健康检查失败。
func TestValidateScriptContent(t *testing.T) {
	dir := t.TempDir()

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if err := validateScriptContent(write("ok.sh", "#!/bin/sh\necho hi\n")); err != nil {
		t.Fatalf("正常脚本应通过: %v", err)
	}
	if err := validateScriptContent(write("ok.ps1", "# comment\nWrite-Output hi\n")); err != nil {
		t.Fatalf("PowerShell 脚本应通过: %v", err)
	}

	if err := validateScriptContent(write("empty.ps1", "")); err == nil ||
		!strings.Contains(err.Error(), "为空") {
		t.Fatalf("空文件应拒绝，got %v", err)
	}
	for _, html := range []string{
		"<!DOCTYPE html><html><body>blocked</body></html>",
		"<html><head><title>502</title></head></html>",
		"  \n<HTML>oops</HTML>",
	} {
		err := validateScriptContent(write("bad.html", html))
		if err == nil || !strings.Contains(err.Error(), "HTML") {
			t.Fatalf("HTML 页面应拒绝（%q），got %v", html[:20], err)
		}
	}
	if err := validateScriptContent(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("文件不存在应报错")
	}
}
