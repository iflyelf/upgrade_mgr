package cache

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNormalizePrefix(t *testing.T) {
	cases := map[string]string{
		"":             "upgrade_mgr:",
		"  ":           "upgrade_mgr:",
		"upgrade_mgr":  "upgrade_mgr:",
		"upgrade_mgr:": "upgrade_mgr:",
		"upgrade mgr":  "upgrade_mgr:",
		"tenant a":     "tenant_a:",
	}
	for in, want := range cases {
		if got := normalizePrefix(in); got != want {
			t.Errorf("normalizePrefix(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestNormalizeAddrs(t *testing.T) {
	got := normalizeAddrs(Options{Addrs: []string{"a:7000", " b:7001 ", ""}})
	if len(got) != 2 || got[0] != "a:7000" || got[1] != "b:7001" {
		t.Fatalf("unexpected addrs: %#v", got)
	}
	got = normalizeAddrs(Options{Host: "10.1.230.44", Port: 7000})
	if len(got) != 1 || got[0] != "10.1.230.44:7000" {
		t.Fatalf("host/port fallback: %#v", got)
	}
	got = normalizeAddrs(Options{Host: "redis.internal"})
	if len(got) != 1 || got[0] != "redis.internal:6379" {
		t.Fatalf("default port: %#v", got)
	}
	got = normalizeAddrs(Options{
		Host:  "ignored",
		Port:  1,
		Addrs: []string{"10.1.230.44:7000"},
	})
	if len(got) != 1 || got[0] != "10.1.230.44:7000" {
		t.Fatalf("hosts should win over host/port: %#v", got)
	}
}

func TestKeyAndStatusDisabled(t *testing.T) {
	c := New(Options{Enabled: false, Prefix: "demo"})
	if c.Enabled() {
		t.Fatal("disabled cache must report Enabled=false")
	}
	if got := c.Key("http", "abc", "/api/hosts"); got != "demo:http:abc:/api/hosts" {
		t.Fatalf("key=%q", got)
	}
	if _, err := c.GetBytes(context.Background(), "k"); err == nil {
		t.Fatal("GetBytes on disabled cache should error")
	}
	if err := c.SetBytes(context.Background(), "k", []byte("v"), time.Second); err == nil {
		t.Fatal("SetBytes on disabled cache should error")
	}
	st := c.Status()
	if st.Enabled || st.Prefix != "demo:" {
		t.Fatalf("status=%+v", st)
	}
}

func TestNewWithoutAddrReportsError(t *testing.T) {
	c := New(Options{Enabled: true, Mode: "single"})
	if c.Enabled() {
		t.Fatal("no addr should disable cache")
	}
	if !strings.Contains(c.Status().LastError, "地址为空") {
		t.Fatalf("last_error=%q", c.Status().LastError)
	}
}

func TestSentinelRequiresMaster(t *testing.T) {
	c := New(Options{Enabled: true, Mode: "sentinel", Addrs: []string{"127.0.0.1:26379"}})
	if c.Enabled() {
		t.Fatal("sentinel without master must disable cache")
	}
	if !strings.Contains(c.Status().LastError, "master_name") {
		t.Fatalf("last_error=%q", c.Status().LastError)
	}
}

func TestKeyHashStable(t *testing.T) {
	a := KeyHash("page=1&size=50")
	b := KeyHash("page=1&size=50")
	if a != b || len(a) != 24 {
		t.Fatalf("hash not stable or wrong length: %q %q", a, b)
	}
	if a == KeyHash("page=2&size=50") {
		t.Fatal("different input must produce different hash")
	}
}
