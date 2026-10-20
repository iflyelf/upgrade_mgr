package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateInstallPath_DenyList(t *testing.T) {
	deny := []string{
		"/etc/passwd", "/etc/cron.d/backdoor", "/boot/vmlinuz",
		"/sys/kernel/x", "/proc/1/mem", "/dev/sda", "/root/.ssh/authorized_keys",
		"/bin/ls", "/sbin/init", "/lib/x.so", "/run/x", "/var/run/x",
	}
	for _, p := range deny {
		if err := validateInstallPath(p); err == nil {
			t.Errorf("应拒绝危险路径但通过了: %s", p)
		}
	}
}

func TestValidateInstallPath_Allow(t *testing.T) {
	allow := []string{
		"/opt/app/bin", "/usr/local/bin/app", "/data/app/bin",
		"/srv/myapp/bin", "/var/lib/myapp/bin", "/home/u/app",
	}
	for _, p := range allow {
		if err := validateInstallPath(p); err != nil {
			t.Errorf("应允许合法部署路径但被拒: %s (%v)", p, err)
		}
	}
}

func TestValidateInstallPath_Traversal(t *testing.T) {
	bad := []string{"/opt/../etc/passwd", "relative/path", "", "/opt/a/../../etc/x"}
	for _, p := range bad {
		if err := validateInstallPath(p); err == nil {
			t.Errorf("应拒绝但通过了: %s", p)
		}
	}
}

// TestValidateInstallPath_TempDirAllowed 覆盖 e2e 使用 t.TempDir() 的场景，
// 防止安全校验误杀测试与临时部署路径（此前导致 e2e 失败的回归）。
func TestValidateInstallPath_TempDirAllowed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "demo_bin")
	if err := validateInstallPath(p); err != nil {
		t.Errorf("t.TempDir() 路径应被允许: %s (%v)", p, err)
	}
}

// TestValidateInstallPath_SymlinkEscape 验证符号链接逃逸被拦截。
func TestValidateInstallPath_SymlinkEscape(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "evil")
	if err := os.Symlink("/etc", link); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	target := filepath.Join(link, "passwd")
	if err := validateInstallPath(target); err == nil {
		t.Errorf("应拦截符号链接逃逸到 /etc，但通过了: %s", target)
	}
}
