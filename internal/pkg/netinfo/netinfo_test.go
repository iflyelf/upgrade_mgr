package netinfo

import "testing"

func TestNormalizeIP(t *testing.T) {
	cases := map[string]string{
		"10.1.2.3":      "10.1.2.3",
		" 10.1.2.3 ":    "10.1.2.3",
		"[2001:db8::1]": "2001:db8::1",
		"":              "",
		"not-an-ip":     "",
		"10.1.2.0/24":   "",
	}
	for in, want := range cases {
		if got := NormalizeIP(in); got != want {
			t.Errorf("NormalizeIP(%q)=%q want %q", in, got, want)
		}
	}
}

func TestExitIPFromServerURL(t *testing.T) {
	// 本机到 127.0.0.1 的出口是回环地址，probeUDP 会拒绝，退化为枚举网卡；
	// 测试只要求不 panic 且不返回回环地址。
	got := ExitIPFromServerURL("http://127.0.0.1:8090")
	if got == "127.0.0.1" || got == "::1" {
		t.Fatalf("不应返回回环地址: %q", got)
	}
	t.Logf("exit ip: %q", got)
}

func TestHostPort(t *testing.T) {
	cases := map[string]string{
		"https://mgr.example.com":   "mgr.example.com:443",
		"http://mgr.example.com":    "mgr.example.com:80",
		"http://10.0.0.5:8090":      "10.0.0.5:8090",
		"10.0.0.5:8090":             "10.0.0.5:8090",
		"10.0.0.5":                  "10.0.0.5:80",
		"":                          "",
		"http://[2001:db8::1]:8090": "[2001:db8::1]:8090",
	}
	for in, want := range cases {
		if got := hostPort(in); got != want {
			t.Errorf("hostPort(%q)=%q want %q", in, got, want)
		}
	}
}
