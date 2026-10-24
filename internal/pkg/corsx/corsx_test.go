package corsx

import "testing"

func TestNormalizeOrigins(t *testing.T) {
	got, err := NormalizeOrigins([]string{" HTTPS://OPS.EXAMPLE.COM/ ", "https://ops.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "https://ops.example.com" {
		t.Fatalf("got %#v", got)
	}
	for _, bad := range []string{"*", "ftp://x", "https://x/path", "https://user@x"} {
		if _, err := NormalizeOrigins([]string{bad}); err == nil {
			t.Errorf("非法 Origin %q 应被拒绝", bad)
		}
	}
}

func TestMatch(t *testing.T) {
	allowed := []string{"https://ops.example.com", "http://localhost:5173"}
	if !Match(allowed, "https://OPS.example.com/") {
		t.Fatal("大小写/尾斜杠规范化后应命中")
	}
	if Match(allowed, "https://evil.example.com") {
		t.Fatal("非白名单来源不应命中")
	}
}
