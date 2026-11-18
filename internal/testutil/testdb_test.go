package testutil

import (
	"strings"
	"testing"
)

// TestIsTestDSN 生产库连接串必须被判定为不安全，测试库/测试 schema 才放行。
func TestIsTestDSN(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want bool
	}{
		{"生产关键字 DSN", "host=10.0.51.88 port=6000 user=u password=p dbname=upgrade_mgr sslmode=disable", false},
		{"生产 URL DSN", "postgres://u:p@10.0.51.88:6000/upgrade_mgr?sslmode=disable", false},
		{"本地关键字 DSN", "host=127.0.0.1 port=5432 user=u dbname=upgrade_mgr sslmode=disable", true},
		{"本地 URL DSN", "postgres://u:p@localhost:5432/upgrade_mgr?sslmode=disable", true},
		{"IPv6 回环", "host=::1 port=5432 user=u dbname=upgrade_mgr sslmode=disable", true},
		{"测试库名", "host=10.0.51.88 user=u dbname=upgrade_mgr_test sslmode=disable", true},
		{"测试 schema", "host=10.0.51.88 user=u dbname=upgrade_mgr sslmode=disable search_path=upgrade_mgr_test,public", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isTestDSN(c.dsn); got != c.want {
				t.Fatalf("isTestDSN(%q)=%v, want %v", c.dsn, got, c.want)
			}
		})
	}
}

// TestWithSearchPath 兼容 URL 与关键字两种 DSN，并覆盖原有 search_path。
func TestWithSearchPath(t *testing.T) {
	kw := WithSearchPath("host=10.0.0.1 user=u dbname=db search_path=old,public sslmode=disable", "svc_test")
	if strings.Contains(kw, "search_path=old") || !strings.Contains(kw, "search_path=svc_test") {
		t.Fatalf("关键字 DSN 未正确替换 search_path: %s", kw)
	}
	u := WithSearchPath("postgres://u:p@10.0.0.1/db?search_path=old", "svc_test")
	if !strings.Contains(u, "search_path=svc_test") || strings.Contains(u, "search_path=old") {
		t.Fatalf("URL DSN 未正确替换 search_path: %s", u)
	}
}

// TestRedactDSN 跳过日志不能泄露密码。
func TestRedactDSN(t *testing.T) {
	if got := redactDSN("host=h user=u password=secret dbname=d"); strings.Contains(got, "secret") {
		t.Fatalf("密码未脱敏: %s", got)
	}
	if got := redactDSN("postgres://u:secret@h/db"); strings.Contains(got, "secret") {
		t.Fatalf("URL 密码未脱敏: %s", got)
	}
}
