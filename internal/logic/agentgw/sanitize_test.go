package agentgw

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeLogRemovesNULAndControls(t *testing.T) {
	in := "下载完成\x00\n脚本输出:\x1b[31m错误\x1b[0m\r\n"
	out := sanitizeLog(in)
	if strings.ContainsRune(out, 0) {
		t.Fatal("日志仍包含 NUL")
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Fatal("日志仍包含 ANSI ESC")
	}
	if !strings.Contains(out, "下载完成") || !strings.Contains(out, "脚本输出") {
		t.Fatalf("正常内容被误删: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatal("清洗后不是合法 UTF-8")
	}
}

func TestSanitizeLogReplacesInvalidUTF8(t *testing.T) {
	in := string([]byte{'a', 0xff, 0xfe, 'b'})
	out := sanitizeLog(in)
	if !utf8.ValidString(out) {
		t.Fatalf("清洗后仍是非法 UTF-8: %q", out)
	}
	if !strings.HasPrefix(out, "a") || !strings.HasSuffix(out, "b") {
		t.Fatalf("非法字节替换破坏了边界: %q", out)
	}
}

func TestSanitizeAndTruncateKeepsValidUTF8(t *testing.T) {
	in := strings.Repeat("升级成功", 1000)
	out := sanitizeAndTruncate(in, 101)
	if len(out) > 101 {
		t.Fatalf("截断后超长: %d", len(out))
	}
	if !utf8.ValidString(out) {
		t.Fatal("截断破坏了 UTF-8")
	}
}

func TestNormalizeVersionTruncatesToColumnWidth(t *testing.T) {
	v := normalizeVersion(strings.Repeat("v", 100) + "\x00")
	if len(v) > maxVersionBytes {
		t.Fatalf("版本超长: %d", len(v))
	}
	if strings.ContainsRune(v, 0) {
		t.Fatal("版本包含 NUL")
	}
}

func TestVersionFromSuccessMessage(t *testing.T) {
	if got := versionFromSuccessMessage("升级成功，当前版本: 1.8.2"); got != "1.8.2" {
		t.Fatalf("版本解析失败: %q", got)
	}
	if got := versionFromSuccessMessage("升级成功"); got != "" {
		t.Fatalf("无版本时不应返回内容: %q", got)
	}
}
