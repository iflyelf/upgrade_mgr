package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

func uploadMultipart(t *testing.T, h http.HandlerFunc, name, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/external/binaries/"+name+"/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestExternalBinaryOfflineUpload 离线文件优先于上游缓存，可列出并删除。
func TestExternalBinaryOfflineUpload(t *testing.T) {
	db, _ := setupHostDB(t)
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Server.AgentBinaryDir = dir
	cfg.Server.ExternalBinaries = map[string]string{
		"offline-tool": "https://github.com/example/offline-tool.exe",
	}
	sc := &svc.ServiceContext{DB: db, Config: cfg}

	// local=1 且未上传：立即 404，绝不触发上游下载。
	req0 := httptest.NewRequest("GET", "/api/external/binary/offline-tool?local=1", nil)
	req0.SetPathValue("name", "offline-tool")
	rec0 := httptest.NewRecorder()
	ExternalBinaryDownload(sc)(rec0, req0)
	if rec0.Code != http.StatusNotFound {
		t.Fatalf("local=1 未上传应 404，实际 %d: %s", rec0.Code, rec0.Body.String())
	}

	payload := []byte("MZoffline-binary-content")
	rec := uploadMultipart(t, UploadExternalBinary(sc), "offline-tool", "tool.exe", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("上传失败 %d: %s", rec.Code, rec.Body.String())
	}
	var up struct {
		Data struct {
			Local     bool  `json:"local"`
			LocalSize int64 `json:"local_size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	if !up.Data.Local || up.Data.LocalSize != int64(len(payload)) {
		t.Fatalf("上传响应不符: %+v", up.Data)
	}

	// 下载：应直接返回本地文件，不访问上游。
	req := httptest.NewRequest("GET", "/api/external/binary/offline-tool?local=1", nil)
	req.SetPathValue("name", "offline-tool")
	rec = httptest.NewRecorder()
	ExternalBinaryDownload(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("下载失败 %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("下载内容不符: %q", rec.Body.String())
	}

	// 列表标记 local
	req = httptest.NewRequest("GET", "/api/external/binaries", nil)
	rec = httptest.NewRecorder()
	ListExternalBinaries(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表失败 %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Data struct {
			Items []struct {
				Name  string `json:"name"`
				Local bool   `json:"local"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data.Items) != 1 || list.Data.Items[0].Name != "offline-tool" || !list.Data.Items[0].Local {
		t.Fatalf("列表不符: %+v", list.Data.Items)
	}

	// 删除本地文件
	req = httptest.NewRequest("DELETE", "/api/external/binaries/offline-tool/upload", nil)
	req.SetPathValue("name", "offline-tool")
	rec = httptest.NewRecorder()
	DeleteExternalBinaryUpload(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除失败 %d: %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/external/binaries", nil)
	rec = httptest.NewRecorder()
	ListExternalBinaries(sc)(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Data.Items) != 1 || list.Data.Items[0].Local {
		t.Fatalf("删除后仍标记 local: %+v", list.Data.Items)
	}
}

// TestExternalBinaryUploadRejectsHTML 二进制上传必须是 MZ；脚本不能是 HTML。
func TestExternalBinaryUploadRejectsHTML(t *testing.T) {
	db, _ := setupHostDB(t)
	cfg := &config.Config{}
	cfg.Server.AgentBinaryDir = t.TempDir()
	cfg.Server.ExternalBinaries = map[string]string{
		"offline-tool":   "https://github.com/example/tool.exe",
		"offline-script": "script:https://raw.githubusercontent.com/example/install.ps1",
	}
	sc := &svc.ServiceContext{DB: db, Config: cfg}

	rec := uploadMultipart(t, UploadExternalBinary(sc), "offline-tool", "tool.exe", []byte("<html>error</html>"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非 MZ 应 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
	rec = uploadMultipart(t, UploadExternalBinary(sc), "offline-script", "install.ps1", []byte("<!DOCTYPE html><html></html>"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("HTML 脚本应 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
	rec = uploadMultipart(t, UploadExternalBinary(sc), "offline-script", "install.ps1", []byte("Write-Host 'ok'"))
	if rec.Code != http.StatusOK {
		t.Fatalf("合法脚本应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestProxyFallbackURL GitHub/raw 才生成代理回退地址。
func TestProxyFallbackURL(t *testing.T) {
	if got := proxyFallbackURL("https://github.com/a/b/releases/download/latest/x.exe"); got !=
		"https://proxy.xiaonuo.live/?url=https://github.com/a/b/releases/download/latest/x.exe" {
		t.Fatalf("github 回退地址不符: %s", got)
	}
	if got := proxyFallbackURL("https://raw.githubusercontent.com/a/b/main/x.ps1"); got == "" {
		t.Fatal("raw 应生成回退地址")
	}
	if got := proxyFallbackURL("https://example.com/x.exe"); got != "" {
		t.Fatalf("非 GitHub 不应回退: %s", got)
	}
}

// TestExternalBinaryScriptServedWithBOM 脚本下载必须自带 UTF-8 BOM。
//
// Windows PowerShell 2.0 对无 BOM 的 .ps1 按系统 ANSI(GBK) 解码，含中文会报
// UnexpectedToken；平台/浏览器/任务示例直接下载时都应拿到 BOM 版本。
func TestExternalBinaryScriptServedWithBOM(t *testing.T) {
	db, _ := setupHostDB(t)
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Server.AgentBinaryDir = dir
	cfg.Server.ExternalBinaries = map[string]string{
		"install-test.ps1": "script:https://raw.githubusercontent.com/example/install.ps1",
	}
	sc := &svc.ServiceContext{DB: db, Config: cfg}

	content := []byte("# 中文注释\nWrite-Output 'ok'\n")
	if rec := uploadMultipart(t, UploadExternalBinary(sc), "install-test.ps1", "install.ps1", content); rec.Code != http.StatusOK {
		t.Fatalf("上传失败 %d: %s", rec.Code, rec.Body.String())
	}
	for _, query := range []string{"", "?local=1"} {
		req := httptest.NewRequest("GET", "/api/external/binary/install-test.ps1"+query, nil)
		req.SetPathValue("name", "install-test.ps1")
		rec := httptest.NewRecorder()
		ExternalBinaryDownload(sc)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 下载失败 %d: %s", query, rec.Code, rec.Body.String())
		}
		body := rec.Body.Bytes()
		if !bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}) {
			n := len(body)
			if n > 8 {
				n = 8
			}
			t.Fatalf("%s 下载内容缺少 UTF-8 BOM: %x", query, body[:n])
		}
		if !bytes.Equal(body[3:], content) {
			t.Fatalf("%s 下载内容不符: %q", query, body[3:])
		}
	}

	// 已带 BOM 的脚本不能出现双 BOM。
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, content...)
	if rec := uploadMultipart(t, UploadExternalBinary(sc), "install-test.ps1", "install.ps1", withBOM); rec.Code != http.StatusOK {
		t.Fatalf("上传带 BOM 脚本失败 %d: %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest("GET", "/api/external/binary/install-test.ps1?local=1", nil)
	req.SetPathValue("name", "install-test.ps1")
	rec := httptest.NewRecorder()
	ExternalBinaryDownload(sc)(rec, req)
	body := rec.Body.Bytes()
	if len(body) < 6 || !bytes.Equal(body[:3], []byte{0xEF, 0xBB, 0xBF}) ||
		bytes.Equal(body[3:6], []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("BOM 处理错误（应恰好一个 BOM）: %x", body[:minInt(9, len(body))])
	}
}

// TestExternalBinaryNonPowerShellScriptNoBOM 非 PowerShell 脚本不能补 BOM。
//
// .sh / .py / .cmd 带 BOM 会被解释器当成内容（例如 bash 报 ": No such file"），
// 只有 .ps1/.psm1 需要 BOM。
func TestExternalBinaryNonPowerShellScriptNoBOM(t *testing.T) {
	db, _ := setupHostDB(t)
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Server.AgentBinaryDir = dir
	cfg.Server.ExternalBinaries = map[string]string{
		"install.sh":  "script:https://raw.githubusercontent.com/example/install.sh",
		"install.bat": "script:https://raw.githubusercontent.com/example/install.bat",
	}
	sc := &svc.ServiceContext{DB: db, Config: cfg}

	for _, name := range []string{"install.sh", "install.bat"} {
		content := []byte("echo hi\n")
		if rec := uploadMultipart(t, UploadExternalBinary(sc), name, name, content); rec.Code != http.StatusOK {
			t.Fatalf("%s 上传失败 %d: %s", name, rec.Code, rec.Body.String())
		}
		req := httptest.NewRequest("GET", "/api/external/binary/"+name+"?local=1", nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		ExternalBinaryDownload(sc)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 下载失败 %d", name, rec.Code)
		}
		if !bytes.Equal(rec.Body.Bytes(), content) {
			t.Fatalf("%s 不应添加 BOM: %q", name, rec.Body.Bytes())
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
