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
	req := httptest.NewRequest("GET", "/api/external/binary/offline-tool", nil)
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
