package agentupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTargetAndVariant(t *testing.T) {
	dir := t.TempDir()
	body := []byte("fake-agent-binary")
	if err := os.WriteFile(filepath.Join(dir, "upgrade-agent-linux-amd64"), body, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "upgrade-agent-linux-amd64-legacy"), body, 0755); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(dir, "v1.2.3")

	got, err := r.Resolve("amd64", "", "https://upgrade.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if got.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 不符: %s", got.SHA256)
	}
	if got.Size != int64(len(body)) {
		t.Fatalf("size 不符: %d", got.Size)
	}
	if got.URL != "https://upgrade.example.com/api/agent/binary/linux-amd64" {
		t.Fatalf("URL 不符: %s", got.URL)
	}

	legacy, err := r.Resolve("amd64-legacy", "", "https://upgrade.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Variant != "legacy" || legacy.URL != "https://upgrade.example.com/api/agent/binary/linux-amd64-legacy" {
		t.Fatalf("legacy 解析不符: %+v", legacy)
	}

	available := r.Available("https://upgrade.example.com")
	if len(available) != 2 {
		t.Fatalf("应发现 2 个二进制，实际 %d", len(available))
	}
}

func TestResolveRejectsTraversalAndInvalidArch(t *testing.T) {
	r := NewResolver(t.TempDir(), "v1")
	if _, err := r.Resolve("../etc", "", "http://x"); err == nil {
		t.Fatal("非法架构应报错")
	}
	if _, err := r.Resolve("amd64", "weird", "http://x"); err == nil {
		t.Fatal("非法变体应报错")
	}
}

func TestResolveWindowsTargetAndAvailable(t *testing.T) {
	dir := t.TempDir()
	body := []byte("fake-windows-agent")
	for _, name := range []string{
		"upgrade-agent-windows-amd64.exe",
		"upgrade-agent-windows-arm64.exe",
		"upgrade-agent-linux-amd64",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0755); err != nil {
			t.Fatal(err)
		}
	}
	r := NewResolver(dir, "v2.0.0")

	win, err := r.ResolveOS("windows", "amd64", "", "https://mgr.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if win.Filename() != "upgrade-agent-windows-amd64.exe" {
		t.Fatalf("Windows 文件名不符: %s", win.Filename())
	}
	if win.URL != "https://mgr.example.com/api/agent/binary/windows-amd64" {
		t.Fatalf("Windows URL 不符: %s", win.URL)
	}

	// Windows 无 legacy 构建，且不支持 386。
	if _, err := r.ResolveOS("windows", "amd64", "legacy", "https://mgr.example.com"); err == nil {
		t.Fatal("Windows legacy 变体应被拒绝")
	}
	if _, err := r.ResolveOS("windows", "386", "", "https://mgr.example.com"); err == nil {
		t.Fatal("Windows 386 应被拒绝")
	}

	available := r.Available("https://mgr.example.com")
	if len(available) != 3 {
		t.Fatalf("应发现 3 个二进制（2 Windows + 1 Linux），实际 %d: %+v", len(available), available)
	}
	// 排序：linux 在前，windows 在后
	if available[0].OS != OSLinux || available[len(available)-1].OS != OSWindows {
		t.Fatalf("排序不符: %+v", available)
	}
}

func TestChooseOS(t *testing.T) {
	cases := map[string]string{
		"windows": "windows", "Windows": "windows", " winDOWS ": "windows",
		"rhel": OSLinux, "debian": OSLinux, "": OSLinux,
	}
	for in, want := range cases {
		if got := ChooseOS(in); got != want {
			t.Errorf("ChooseOS(%q)=%q want %q", in, got, want)
		}
	}
}

func TestParseBinaryName(t *testing.T) {
	cases := []struct {
		name, osName, arch, variant string
		ok                          bool
	}{
		{"upgrade-agent-linux-amd64", OSLinux, "amd64", "", true},
		{"upgrade-agent-linux-amd64-legacy", OSLinux, "amd64", "legacy", true},
		{"upgrade-agent-windows-amd64.exe", OSWindows, "amd64", "", true},
		{"upgrade-agent-windows-arm64.exe", OSWindows, "arm64", "", true},
		{"upgrade-agent-darwin-amd64", "", "", "", false},
		{"other-file", "", "", "", false},
	}
	for _, c := range cases {
		osName, arch, variant, ok := parseBinaryName(c.name)
		if ok != c.ok || osName != c.osName || arch != c.arch || variant != c.variant {
			t.Errorf("parseBinaryName(%q)=(%q,%q,%q,%v) want (%q,%q,%q,%v)",
				c.name, osName, arch, variant, ok, c.osName, c.arch, c.variant, c.ok)
		}
	}
}

func TestChooseVariant(t *testing.T) {
	cases := []struct {
		reported, kernel, want string
	}{
		{"legacy", "5.10", "legacy"},
		{"", "2.6.32-754.el6.x86_64", "legacy"},
		{"", "3.10.0-1160.el7.x86_64", ""},
		{"", "5.15.0", ""},
		{"normal", "2.6.32", ""},
	}
	for _, c := range cases {
		if got := ChooseVariant(c.reported, c.kernel); got != c.want {
			t.Fatalf("ChooseVariant(%q,%q)=%q want %q", c.reported, c.kernel, got, c.want)
		}
	}
}
