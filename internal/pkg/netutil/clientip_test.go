package netutil

import (
	"net/http/httptest"
	"testing"
)

func mustProxies(t *testing.T, list ...string) *ProxyList {
	t.Helper()
	p, err := ParseTrustedProxies(list)
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	return p
}

func TestClientIP(t *testing.T) {
	trusted := []string{"10.0.0.0/8", "192.168.1.1"}
	cases := []struct {
		name       string
		remote     string
		xff        []string
		xri        string
		trustProxy bool
		trusted    []string
		want       string
	}{
		{name: "不信任代理时忽略 XFF", remote: "10.0.0.7:123", xff: []string{"1.2.3.4"}, want: "10.0.0.7"},
		{name: "不信任代理时忽略 X-Real-IP", remote: "10.0.0.7:123", xri: "1.2.3.4", want: "10.0.0.7"},
		{name: "无受信列表取最右值", remote: "10.0.0.7:123", xff: []string{"6.6.6.6, 1.2.3.4"}, trustProxy: true, want: "1.2.3.4"},
		{name: "无受信列表最右值非法回退 RemoteAddr", remote: "10.0.0.7:123", xff: []string{"1.2.3.4, bogus"}, trustProxy: true, want: "10.0.0.7"},
		{name: "跳过受信代理取第一个非受信", remote: "10.0.0.7:123", xff: []string{"6.6.6.6, 1.2.3.4, 10.1.1.1, 192.168.1.1"}, trustProxy: true, trusted: trusted, want: "1.2.3.4"},
		{name: "多个 XFF 头按顺序合并", remote: "10.0.0.7:123", xff: []string{"6.6.6.6", "1.2.3.4, 10.1.1.1"}, trustProxy: true, trusted: trusted, want: "1.2.3.4"},
		{name: "全部受信取最左", remote: "10.0.0.7:123", xff: []string{"10.2.2.2, 10.1.1.1"}, trustProxy: true, trusted: trusted, want: "10.2.2.2"},
		{name: "非法条目截断后退回最近受信", remote: "10.0.0.7:123", xff: []string{"1.2.3.4, bogus, 10.1.1.1"}, trustProxy: true, trusted: trusted, want: "10.1.1.1"},
		{name: "直连对端不受信时忽略转发头", remote: "8.8.8.8:123", xff: []string{"1.2.3.4"}, trustProxy: true, trusted: trusted, want: "8.8.8.8"},
		{name: "无 XFF 时使用 X-Real-IP", remote: "10.0.0.7:123", xri: "1.2.3.4", trustProxy: true, trusted: trusted, want: "1.2.3.4"},
		{name: "X-Real-IP 非法回退 RemoteAddr", remote: "10.0.0.7:123", xri: "nope", trustProxy: true, want: "10.0.0.7"},
		{name: "无端口 RemoteAddr", remote: "10.0.0.7", trustProxy: true, want: "10.0.0.7"},
		{name: "IPv6", remote: "[::1]:123", xff: []string{"2001:db8::1, ::1"}, trustProxy: true, trusted: []string{"::1"}, want: "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if tc.xri != "" {
				r.Header.Set("X-Real-IP", tc.xri)
			}
			var p *ProxyList
			if tc.trusted != nil {
				p = mustProxies(t, tc.trusted...)
			}
			if got := ClientIPWithProxies(r, tc.trustProxy, p); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseTrustedProxies_Invalid(t *testing.T) {
	p, err := ParseTrustedProxies([]string{"10.0.0.0/8", "not-an-ip", "", "1.2.3.4/99"})
	if err == nil {
		t.Fatal("期望返回非法条目错误")
	}
	if len(p.nets) != 1 {
		t.Fatalf("合法条目应保留 1 个，实际 %d", len(p.nets))
	}
}
