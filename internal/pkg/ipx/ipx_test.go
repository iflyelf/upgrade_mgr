package ipx

import (
	"strings"
	"testing"
)

func TestParseSingle(t *testing.T) {
	got, err := Parse("10.1.255.38")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 1 || got[0] != "10.1.255.38" {
		t.Fatalf("got %v", got)
	}
}

func TestParseShortRange(t *testing.T) {
	got, err := Parse("10.1.255.24-26")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"10.1.255.24", "10.1.255.25", "10.1.255.26"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseFullRange(t *testing.T) {
	got, err := Parse("10.1.26.5-10.1.26.7")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := "10.1.26.5,10.1.26.6,10.1.26.7"
	if strings.Join(got, ",") != want {
		t.Fatalf("got %v", got)
	}
}

func TestParseCIDR(t *testing.T) {
	got, err := Parse("10.1.255.0/30")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// /30 => .1 .2 (去掉网络 .0 与广播 .3)
	want := "10.1.255.1,10.1.255.2"
	if strings.Join(got, ",") != want {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseMixedAndDedup(t *testing.T) {
	// 需求原文示例 + 重叠去重
	got, err := Parse("10.1.255.24-26,10.1.255.38,10.1.255.0/24,10.1.26.5-10.1.26.7")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// /24 覆盖了 .24-26 与 .38，应去重；总数 = 254(/24) + 3(10.1.26.5-7)
	if len(got) != 254+3 {
		t.Fatalf("expected %d ips, got %d", 254+3, len(got))
	}
	// 校验排序：第一个应为 10.1.26.5
	if got[0] != "10.1.26.5" {
		t.Fatalf("sort broken, first=%s", got[0])
	}
}

func TestParseWithExclude(t *testing.T) {
	got, err := ParseWithExclude("10.1.255.0/29", "10.1.255.1,10.1.255.2", 0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// /29 hosts = .1..6，排除 .1 .2 => .3 .4 .5 .6
	want := "10.1.255.3,10.1.255.4,10.1.255.5,10.1.255.6"
	if strings.Join(got, ",") != want {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseMaxExpand(t *testing.T) {
	// /8 将产生 16M+ IP，应在计算阶段拒绝
	_, err := ParseWithExclude("10.0.0.0/8", "", 1024)
	if err == nil {
		t.Fatal("expected max-expand error for /8")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("unexpected err: %v", err)
	}

	// 大范围 range 也应拒绝
	_, err = ParseWithExclude("10.0.0.1-10.1.0.1", "", 1024)
	if err == nil {
		t.Fatal("expected max-expand error for large range")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("unexpected err: %v", err)
	}

	// 边界：恰好 65536 应成功（默认上限）
	got, err := ParseWithExclude("10.0.0.0/16", "", 0)
	if err != nil {
		t.Fatalf("unexpected err for /16: %v", err)
	}
	if len(got) != 65534 { // /16 去掉网络和广播地址
		t.Fatalf("expected 65534 IPs for /16, got %d", len(got))
	}

	// 超过默认上限应失败
	_, err = ParseWithExclude("10.0.0.0/15", "", 0)
	if err == nil {
		t.Fatal("expected max-expand error for /15")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestParseInvalid(t *testing.T) {
	cases := []string{"", "999.1.1.1", "10.1.1.1-10.1.1.0", "10.1.1.0/33", "10.1.255.24-300"}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}
