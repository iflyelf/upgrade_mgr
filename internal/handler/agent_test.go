package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildInstallCommandRemovesStaleBinary(t *testing.T) {
	cmd := buildInstallCommand("http://10.1.230.50:8090", "amd64", "tok-123", false)

	if !strings.HasPrefix(cmd, "rm -f /tmp/upgrade-agent") {
		t.Fatalf("安装命令必须先删除旧二进制，got:\n%s", cmd)
	}
	for _, want := range []string{
		"wget -c",
		"'http://10.1.230.50:8090/api/agent/binary/linux-amd64'",
		"chmod +x /tmp/upgrade-agent",
		"install --server 'http://10.1.230.50:8090' --enroll-token 'tok-123'",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("安装命令缺少 %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "--force") {
		t.Fatalf("普通安装不应带 --force:\n%s", cmd)
	}
}

func TestInstallBaseURLPrefersExternalURL(t *testing.T) {
	r := httptest.NewRequest("GET", "http://internal:8090/api/agent/enroll-token", nil)
	r.Host = "10.1.230.50:8090"

	if got := installBaseURL(" https://upgrade.example.com/ ", r, false); got != "https://upgrade.example.com" {
		t.Fatalf("external_url 应优先且去掉结尾斜杠，got %q", got)
	}
	if got := installBaseURL("", r, false); got != "http://10.1.230.50:8090" {
		t.Fatalf("留空应回退请求 Host，got %q", got)
	}
}

func TestInstallBaseURLForwardedProtoOnlyWhenTrusted(t *testing.T) {
	r := httptest.NewRequest("GET", "http://internal:8090/api/agent/enroll-token", nil)
	r.Host = "upgrade.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")

	if got := installBaseURL("", r, false); got != "http://upgrade.example.com" {
		t.Fatalf("trust_proxy=false 不应采信 X-Forwarded-Proto，got %q", got)
	}
	if got := installBaseURL("", r, true); got != "https://upgrade.example.com" {
		t.Fatalf("trust_proxy=true 应采信 X-Forwarded-Proto，got %q", got)
	}
}

func TestInstallBaseURLTLS(t *testing.T) {
	r := httptest.NewRequest("GET", "https://upgrade.example.com/api/agent/enroll-token", nil)
	if got := installBaseURL("", r, false); got != "https://upgrade.example.com" {
		t.Fatalf("TLS 请求应生成 https，got %q", got)
	}
}

func TestBuildInstallCommandForce(t *testing.T) {
	cmd := buildInstallCommand("http://10.1.230.50:8090", "amd64", "tok-123", true)
	if !strings.HasSuffix(cmd, "--force") {
		t.Fatalf("强制重新注册命令应以 --force 结尾，got:\n%s", cmd)
	}
	if !strings.Contains(cmd, "--enroll-token 'tok-123' --force") {
		t.Fatalf("--force 必须与 --enroll-token 同时出现，got:\n%s", cmd)
	}
}
