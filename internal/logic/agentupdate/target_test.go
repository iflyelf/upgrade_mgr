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
