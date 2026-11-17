package main

import "testing"

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"update", "-y", "--server", "http://console.example.com/", "--token", "enr_auto_x", "--arch", "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Action != "update" || !o.Yes || o.Server != "http://console.example.com/" ||
		o.Token != "enr_auto_x" || o.Arch != "arm64" {
		t.Fatalf("解析结果不符: %+v", o)
	}
	o, err = parseArgs([]string{"--server=http://a", "--arch=amd64", "uninstall"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Action != "uninstall" || o.Server != "http://a" || o.Arch != "amd64" {
		t.Fatalf("等号参数解析不符: %+v", o)
	}
	if _, err := parseArgs([]string{"install", "--unknown"}); err == nil {
		t.Fatal("未知参数应报错")
	}
}

func TestResolveServer(t *testing.T) {
	t.Setenv("UPGRADE_MGR_EXTERNAL_URL", "http://from-env/")
	if got := resolveServer(""); got != "http://from-env" {
		t.Fatalf("resolveServer env = %q", got)
	}
	if got := resolveServer(" http://explicit/ "); got != "http://explicit" {
		t.Fatalf("resolveServer explicit = %q", got)
	}
}

func TestResolveArch(t *testing.T) {
	t.Setenv("PROCESSOR_ARCHITEW6432", "ARM64")
	if got := resolveArch(""); got != "arm64" {
		t.Fatalf("resolveArch env = %q", got)
	}
	if got := resolveArch("x86_64"); got != "amd64" {
		t.Fatalf("resolveArch explicit = %q", got)
	}
}
