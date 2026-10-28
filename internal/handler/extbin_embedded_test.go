package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/examples"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestExternalBinaryEmbeddedScriptFallback 已保存过白名单（不含 node-exporter-install.sh）、
// 平台不能出网、也没上传离线文件时，?local=1 仍返回随服务端发布的 install.sh。
func TestExternalBinaryEmbeddedScriptFallback(t *testing.T) {
	cfg := config.Default()
	cfg.Server.AgentBinaryDir = t.TempDir()
	// 模拟早期部署：白名单只保存了 windows 条目。
	cfg.Server.ExternalBinaries = map[string]string{
		"windows-exporter-amd64": "https://github.com/iflyelf/windows_exporter/releases/download/latest/windows_exporter-amd64.exe",
	}
	sc := &svc.ServiceContext{Config: cfg}

	req := httptest.NewRequest("GET", "/api/external/binary/node-exporter-install.sh?local=1", nil)
	req.SetPathValue("name", "node-exporter-install.sh")
	rec := httptest.NewRecorder()
	ExternalBinaryDownload(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应返回内置脚本，实际 %d: %s", rec.Code, rec.Body.String())
	}
	want := examples.EmbeddedScript("node-exporter-install.sh")
	if len(want) == 0 || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("内容与内置 install.sh 不一致（%d 字节）", rec.Body.Len())
	}
	if !strings.HasPrefix(rec.Body.String(), "#!") {
		t.Fatal("应为 shell 脚本")
	}

	// 地址被管理员改写过的条目不使用内置兜底（内嵌版本不一定是其期望的脚本）。
	cfg.Server.ExternalBinaries["node-exporter-install.sh"] = "script:https://github.com/mirror/install.sh"
	cfg.Server.AgentBinaryDir = t.TempDir()
	rec = httptest.NewRecorder()
	ExternalBinaryDownload(sc)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("改写地址后 local=1 未命中应 404，实际 %d", rec.Code)
	}
}
