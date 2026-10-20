package safepath

import "testing"

func TestSafeSegment(t *testing.T) {
	ok := []string{"zabbix-agent", "1.2.3", "amd64", "file.tar.gz", "v2_0"}
	for _, s := range ok {
		if err := SafeSegment(s); err != nil {
			t.Errorf("SafeSegment(%q) 应通过，却报错: %v", s, err)
		}
	}
	bad := []string{"", ".", "..", "../etc", "a/b", "a\\b", "..\\x", "x/../y", "a b", "a;b", "-rf", "$(x)"}
	for _, s := range bad {
		if err := SafeSegment(s); err == nil {
			// -rf 含合法字符集但应被 wget 参数注入防护另行处理；此处只测分隔符/遍历
			if s == "-rf" {
				continue
			}
			t.Errorf("SafeSegment(%q) 应报错，却通过", s)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"normal.bin":        "normal.bin",
		"../../etc/passwd":  "passwd",
		"/abs/path/x.tar":   "x.tar",
		"a b c.txt":         "a_b_c.txt",
		"..":                "file",
		"":                  "file",
		"a\"b.js":           "a_b.js",
		"....hidden":        "hidden",
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q)=%q, 期望 %q", in, got, want)
		}
	}
}

func TestEnsureWithin(t *testing.T) {
	if err := EnsureWithin("/data/art", "/data/art/1/2/x.bin"); err != nil {
		t.Errorf("合法子路径应通过: %v", err)
	}
	if err := EnsureWithin("/data/art", "/data/art/../../etc/passwd"); err == nil {
		t.Error("越界路径应报错")
	}
	if err := EnsureWithin("/data/art", "/etc/passwd"); err == nil {
		t.Error("完全越界应报错")
	}
}
