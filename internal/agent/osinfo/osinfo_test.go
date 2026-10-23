//go:build !windows

package osinfo

import (
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	i := Detect()
	t.Logf("OS: %s %s %s (family=%s)", i.OSName, i.OSVersion, i.Arch, i.OSFamily)
	t.Logf("Init: %s, Kernel: %s", i.Init, i.Kernel)
	if i.Arch == "" {
		t.Fatal("arch empty")
	}
	if i.Init == "" {
		t.Fatal("init empty")
	}
	if i.OSFamily == "" {
		t.Fatal("os_family empty")
	}
}

func TestFamilyFromIDs(t *testing.T) {
	cases := []struct{ id, idLike, name, want string }{
		{"centos", "", "", "rhel"},
		{"ubuntu", "debian", "", "debian"},
		{"rhel", "fedora", "", "rhel"},
		{"debian", "", "", "debian"},
		{"alpine", "", "", "alpine"},
		{"unknown", "", "Some Linux", "linux"},
	}
	for _, c := range cases {
		got := familyFromIDs(c.id, c.idLike, c.name)
		if got != c.want {
			t.Errorf("familyFromIDs(%q,%q,%q)=%q want %q", c.id, c.idLike, c.name, got, c.want)
		}
	}
}

func TestExtractVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"CentOS release 6.10 (Final)", "6.10"},
		{"Red Hat Enterprise Linux Server release 7.9 (Maipo)", "7.9"},
		{"no version here", ""},
	}
	for _, c := range cases {
		got := extractVersion(c.in)
		if got != c.want {
			t.Errorf("extractVersion(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestDetectInit(t *testing.T) {
	// 本机测试：至少应识别出 systemd/upstart/sysv/none 之一
	init := detectInit()
	if !strings.Contains("systemd upstart sysv none", init) {
		t.Errorf("unexpected init=%q", init)
	}
	t.Logf("detected init: %s", init)
}
