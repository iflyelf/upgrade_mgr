//go:build !windows

package svcmgr

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidName(t *testing.T) {
	ok := []string{"nginx", "zabbix-agent", "my_app.service"}
	bad := []string{"", ".", "..", "../etc/passwd", "a/b", "-h", "x\x00y"}
	for _, n := range ok {
		if !validName(n) {
			t.Errorf("应合法: %q", n)
		}
	}
	for _, n := range bad {
		if validName(n) {
			t.Errorf("应非法: %q", n)
		}
	}
}

func TestParseLoadState(t *testing.T) {
	cases := map[string]struct {
		state string
		ok    bool
	}{
		"LoadState=loaded\n":    {"loaded", true},
		"LoadState=not-found\n": {"not-found", true},
		"":                      {"", false},
	}
	for in, want := range cases {
		s, ok := parseLoadState(in)
		if s != want.state || ok != want.ok {
			t.Errorf("parseLoadState(%q)=(%q,%v) want (%q,%v)", in, s, ok, want.state, want.ok)
		}
	}
}

func TestSysvAndUpstartExists(t *testing.T) {
	dir := t.TempDir()
	initd := filepath.Join(dir, "init.d")
	upconf := filepath.Join(dir, "init")
	os.MkdirAll(initd, 0755)
	os.MkdirAll(upconf, 0755)

	oldDirs, oldConf := sysvInitDirs, upstartConfDir
	sysvInitDirs, upstartConfDir = []string{initd}, upconf
	defer func() { sysvInitDirs, upstartConfDir = oldDirs, oldConf }()

	os.WriteFile(filepath.Join(initd, "zabbix-agent"), []byte("#!/bin/sh\n"), 0755)
	os.WriteFile(filepath.Join(upconf, "myjob.conf"), []byte("exec /bin/true\n"), 0644)

	sv := &sysvMgr{}
	if !sv.Exists("zabbix-agent") {
		t.Error("sysv 应识别 init.d 脚本")
	}
	if sv.Exists("not-installed") {
		t.Error("sysv 不存在的服务应返回 false")
	}

	up := &upstartMgr{}
	if !up.Exists("myjob") || !up.Exists("zabbix-agent") {
		t.Error("upstart 应识别 upstart job 和 init.d 脚本")
	}
	if up.Exists("not-installed") {
		t.Error("upstart 不存在的服务应返回 false")
	}
	if !up.useSysv("zabbix-agent") || up.useSysv("myjob") {
		t.Error("upstart 仅在无 job 但有 init.d 脚本时走 service 命令")
	}

	if (&noneMgr{}).Exists("anything") {
		t.Error("none 应返回 false")
	}
}
