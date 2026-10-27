package task

import (
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

func TestCommandTargetOS(t *testing.T) {
	cases := []struct {
		name string
		spec protocol.CommandSpec
		want string
	}{
		{"bash", protocol.CommandSpec{Interpreter: "/bin/bash", Script: "echo"}, targetOSLinux},
		{"powershell", protocol.CommandSpec{Interpreter: "powershell.exe", Script: "Write-Host 1"}, targetOSWindows},
		{"cmd full path", protocol.CommandSpec{Interpreter: `C:\Windows\System32\cmd.exe`}, targetOSWindows},
		{"shebang", protocol.CommandSpec{Script: "#!/bin/sh\necho hi"}, targetOSLinux},
		{"unknown", protocol.CommandSpec{Script: "echo hi"}, ""},
	}
	for _, c := range cases {
		if got := commandTargetOS(c.spec); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestComponentTargetOS(t *testing.T) {
	cases := []struct {
		name string
		c    *component.Component
		want string
	}{
		{"os_match windows", &component.Component{OSMatch: "Windows", InstallPath: "/opt/x"}, targetOSWindows},
		{"proxied sh", &component.Component{ScriptURL: "https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/a/b/main/install.sh"}, targetOSLinux},
		{"platform ps1", &component.Component{ScriptURL: "/api/external/binary/windows-exporter-install.ps1"}, targetOSWindows},
		{"windows install path", &component.Component{InstallPath: `C:\Program Files\windows_exporter`}, targetOSWindows},
		{"linux install path", &component.Component{InstallPath: "/opt/monitor/exporter"}, targetOSLinux},
		{"unknown", &component.Component{}, ""},
	}
	for _, c := range cases {
		if got := componentTargetOS(c.c); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestFilterHostsByOS(t *testing.T) {
	hosts := []hostRow{
		{id: 1, osFamily: "rhel"},
		{id: 2, osFamily: "windows"},
		{id: 3, osFamily: ""}, // 系统未知时保留，由 Agent 执行结果判断
		{id: 4, osFamily: "debian"},
	}
	kept, skipped := filterHostsByOS(hosts, targetOSLinux)
	if skipped != 1 || len(kept) != 3 {
		t.Fatalf("linux: kept=%d skipped=%d", len(kept), skipped)
	}
	kept, skipped = filterHostsByOS(hosts, targetOSWindows)
	if skipped != 2 || len(kept) != 2 {
		t.Fatalf("windows: kept=%d skipped=%d", len(kept), skipped)
	}
	kept, skipped = filterHostsByOS(hosts, "")
	if skipped != 0 || len(kept) != 4 {
		t.Fatalf("unknown: kept=%d skipped=%d", len(kept), skipped)
	}
	if len(hosts) != 4 || hosts[1].id != 2 {
		t.Fatal("filterHostsByOS 不应修改原切片")
	}
}
