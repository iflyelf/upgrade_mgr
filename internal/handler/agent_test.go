package handler

import (
	"strings"
	"testing"
)

func TestBuildInstallCommandRemovesStaleBinary(t *testing.T) {
	cmd := buildInstallCommand("http://10.1.230.50:8090", "amd64", "tok-123")

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
}
