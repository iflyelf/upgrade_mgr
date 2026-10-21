package handler

import (
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

func TestBuildInstallCommandForce(t *testing.T) {
	cmd := buildInstallCommand("http://10.1.230.50:8090", "amd64", "tok-123", true)
	if !strings.HasSuffix(cmd, "--force") {
		t.Fatalf("强制重新注册命令应以 --force 结尾，got:\n%s", cmd)
	}
	if !strings.Contains(cmd, "--enroll-token 'tok-123' --force") {
		t.Fatalf("--force 必须与 --enroll-token 同时出现，got:\n%s", cmd)
	}
}
