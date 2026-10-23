package hostnet

import "testing"

func TestNormalizeAddr(t *testing.T) {
	ok := map[string]string{
		"10.8.0.5":                         "10.8.0.5",
		"10.8.0.5:8090":                    "10.8.0.5:8090",
		"https://tunnel.example.com":       "https://tunnel.example.com",
		"https://tunnel.example.com:8443/": "https://tunnel.example.com:8443",
		"tunnel.example.com":               "tunnel.example.com",
		" 10.8.0.5 ":                       "10.8.0.5",
		"":                                 "",
	}
	for in, want := range ok {
		got, err := NormalizeAddr(in)
		if err != nil {
			t.Errorf("NormalizeAddr(%q) 应通过，got err %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeAddr(%q)=%q want %q", in, got, want)
		}
	}

	bad := []string{
		"ftp://tunnel.example.com", // 非 http(s)
		"https://",                 // 缺主机
		"bad host",                 // 非法主机名
		"10.0.0.1:abc",             // 非法端口
		"https://user:pass@t/",     // 不能带凭据
	}
	for _, in := range bad {
		if _, err := NormalizeAddr(in); err == nil {
			t.Errorf("NormalizeAddr(%q) 应报错", in)
		}
	}
}

func TestResolveServerURL(t *testing.T) {
	cases := []struct {
		tunnel, def, want string
	}{
		// 完整 URL 原样使用
		{"https://tunnel.example.com", "http://10.0.0.5:8090", "https://tunnel.example.com"},
		// IP 沿用默认协议与端口
		{"10.8.0.5", "http://10.0.0.5:8090", "http://10.8.0.5:8090"},
		// IP:端口沿用默认协议
		{"10.8.0.5:9000", "https://mgr.example.com", "https://10.8.0.5:9000"},
		// https 默认地址 + 纯 IP：补 443
		{"10.8.0.5", "https://mgr.example.com", "https://10.8.0.5:443"},
		// 默认地址缺端口：http 补 80
		{"10.8.0.5", "http://mgr.example.com", "http://10.8.0.5:80"},
		// 未配置隧道
		{"", "http://10.0.0.5:8090", ""},
	}
	for _, c := range cases {
		if got := ResolveServerURL(c.tunnel, c.def); got != c.want {
			t.Errorf("ResolveServerURL(%q,%q)=%q want %q", c.tunnel, c.def, got, c.want)
		}
	}
	if got := ResolveServerURL("file:///etc/passwd", "http://x:1"); got != "" {
		t.Errorf("非法隧道地址应返回空串，got %q", got)
	}
}

func TestNormalizeExitIP(t *testing.T) {
	if got := NormalizeExitIP(" 10.1.2.3 "); got != "10.1.2.3" {
		t.Errorf("NormalizeExitIP 去空白失败: %q", got)
	}
	if got := NormalizeExitIP("2001:db8::1"); got != "2001:db8::1" {
		t.Errorf("IPv6 应保留: %q", got)
	}
	for _, bad := range []string{"", "not-an-ip", "10.1.2.3/24", "1.2.3"} {
		if got := NormalizeExitIP(bad); got != "" {
			t.Errorf("NormalizeExitIP(%q) 应返回空串，got %q", bad, got)
		}
	}
}
