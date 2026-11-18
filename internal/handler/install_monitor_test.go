package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

func installMonitorTestContext(t *testing.T) (*svc.ServiceContext, string) {
	t.Helper()
	db, _ := setupHostDB(t)
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Server.AgentBinaryDir = dir
	// 每个测试使用独立的设置键：集成测试可能连生产库，绝不能覆盖真实安装令牌。
	oldKey := installMonitorTokenSettingKey
	installMonitorTokenSettingKey = fmt.Sprintf("agent.install_monitor_token_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		db.Exec(`DELETE FROM app_settings WHERE key=$1`, installMonitorTokenSettingKey)
		installMonitorTokenSettingKey = oldKey
	})
	return &svc.ServiceContext{DB: db, Config: cfg}, dir
}

func callInstallMonitor(t *testing.T, h http.HandlerFunc, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func createInstallMonitorToken(t *testing.T, sc *svc.ServiceContext) int64 {
	t.Helper()
	rec := callInstallMonitor(t, InstallMonitorTokenCreate(sc), http.MethodPost, "/api/agent/install-monitor/token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("生成永久令牌失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			ID          int64  `json:"id"`
			TokenPrefix string `json:"token_prefix"`
			BaseURL     string `json:"base_url"`
			LinuxURL    string `json:"linux_url"`
			WindowsURL  string `json:"windows_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.ID <= 0 || out.Data.TokenPrefix == "" ||
		!strings.Contains(out.Data.LinuxURL, "/api/external/binary/install-monitor.sh") ||
		!strings.Contains(out.Data.WindowsURL, "/api/external/binary/install-windows-monitor.exe") {
		t.Fatalf("生成响应异常: %+v", out.Data)
	}
	return out.Data.ID
}

func readInstallMonitorTokenHTTP(t *testing.T, sc *svc.ServiceContext) string {
	t.Helper()
	rec := callInstallMonitor(t, InstallMonitorTokenGet(sc), http.MethodGet, "/api/agent/install-monitor/token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("读取令牌失败 %d: %s", rec.Code, rec.Body.String())
	}
	token := strings.TrimSpace(rec.Body.String())
	if token == "" {
		t.Fatal("读取到的令牌为空")
	}
	return token
}

// TestInstallMonitorTokenLifecycle 验证永久令牌生成、读取与轮换注销。
func TestInstallMonitorTokenLifecycle(t *testing.T) {
	sc, dir := installMonitorTestContext(t)

	// 未生成时脚本读取应自动创建令牌（无需人工点「生成接入脚本」）。
	autoToken := readInstallMonitorTokenHTTP(t, sc)
	if _, ok := consumeTestToken(t, sc.DB, autoToken, "10.1.2.3"); !ok {
		t.Fatal("自动创建的令牌应可用于接入")
	}
	var autoID int64
	if err := sc.DB.QueryRow(`SELECT id FROM enroll_tokens WHERE token_hash=$1`,
		agentgw.HashToken(autoToken)).Scan(&autoID); err != nil {
		t.Fatalf("自动创建的令牌未落库: %v", err)
	}
	t.Cleanup(func() { sc.DB.Exec(`DELETE FROM enroll_tokens WHERE id=$1`, autoID) })

	// 页面 ?format=json 自动展示命令：返回 URL 与令牌前缀，不泄露完整令牌。
	infoRec := callInstallMonitor(t, InstallMonitorTokenGet(sc), http.MethodGet,
		"/api/agent/install-monitor/token?format=json", nil)
	if infoRec.Code != http.StatusOK {
		t.Fatalf("读取脚本信息失败 %d: %s", infoRec.Code, infoRec.Body.String())
	}
	if strings.Contains(infoRec.Body.String(), autoToken) {
		t.Fatal("格式化响应不应包含完整令牌")
	}
	var scriptInfo struct {
		Data struct {
			TokenPrefix string `json:"token_prefix"`
			LinuxURL    string `json:"linux_url"`
			WindowsURL  string `json:"windows_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(infoRec.Body.Bytes(), &scriptInfo); err != nil {
		t.Fatal(err)
	}
	if scriptInfo.Data.TokenPrefix == "" ||
		!strings.Contains(scriptInfo.Data.LinuxURL, "install-monitor.sh") ||
		!strings.Contains(scriptInfo.Data.WindowsURL, "install-windows-monitor.exe") {
		t.Fatalf("脚本信息不完整: %+v", scriptInfo.Data)
	}

	firstID := createInstallMonitorToken(t, sc)
	t.Cleanup(func() { sc.DB.Exec(`DELETE FROM enroll_tokens WHERE id=$1`, firstID) })
	info, err := os.Stat(filepath.Join(dir, ".install-monitor", "token.json"))
	if err != nil {
		t.Fatalf("令牌文件未生成: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("令牌文件权限 = %o, want 600", info.Mode().Perm())
	}
	firstToken := readInstallMonitorTokenHTTP(t, sc)

	// 再次生成：旧令牌注销，新令牌可用。
	secondID := createInstallMonitorToken(t, sc)
	t.Cleanup(func() { sc.DB.Exec(`DELETE FROM enroll_tokens WHERE id=$1`, secondID) })
	if secondID == firstID {
		t.Fatal("轮换后应生成新令牌")
	}
	secondToken := readInstallMonitorTokenHTTP(t, sc)
	if secondToken == firstToken {
		t.Fatal("轮换后令牌内容应变化")
	}
	if _, ok := consumeTestToken(t, sc.DB, firstToken, "10.1.2.3"); ok {
		t.Fatal("轮换后的旧令牌不应能消费")
	}
	if _, ok := consumeTestToken(t, sc.DB, secondToken, "10.1.2.3"); !ok {
		t.Fatal("轮换后的新令牌应可消费")
	}
}

func TestInstallMonitorScriptExternalCache(t *testing.T) {
	sc, _ := installMonitorTestContext(t)
	req := httptest.NewRequest(http.MethodGet, "/api/external/binary/install-monitor.sh?local=1", nil)
	req.SetPathValue("name", "install-monitor.sh")
	rec := httptest.NewRecorder()
	ExternalBinaryDownload(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("脚本应通过外部文件缓存下发，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "install-monitor") ||
		!strings.Contains(rec.Body.String(), "LEGACY-CLEANUP") {
		t.Fatal("脚本内容缺少关键片段")
	}
}

func TestInstallWindowsMonitorExeServing(t *testing.T) {
	sc, dir := installMonitorTestContext(t)
	// 缺失时 404。
	rec := callInstallMonitor(t, InstallWindowsMonitorExe(sc), http.MethodGet, "/api/external/binary/install-windows-monitor.exe", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("缺失时应 404，实际 %d", rec.Code)
	}
	data := []byte("MZfake-windows-installer")
	target := filepath.Join(dir, "install-windows-monitor.exe")
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatal(err)
	}
	rec = callInstallMonitor(t, InstallWindowsMonitorExe(sc), http.MethodGet, "/api/external/binary/install-windows-monitor.exe", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("存在时应 200，实际 %d", rec.Code)
	}
	if string(rec.Body.Bytes()) != string(data) {
		t.Fatal("下载内容不符")
	}
}

// TestInstallMonitorTokenSurvivesMissingFile 多副本部署下，令牌以数据库为准：
// 某个副本没有本地令牌文件时，脚本仍能读取到同一个令牌。
func TestInstallMonitorTokenSurvivesMissingFile(t *testing.T) {
	sc, dir := installMonitorTestContext(t)
	id := createInstallMonitorToken(t, sc)
	t.Cleanup(func() { sc.DB.Exec(`DELETE FROM enroll_tokens WHERE id=$1`, id) })

	first := readInstallMonitorTokenHTTP(t, sc)
	if err := os.RemoveAll(filepath.Join(dir, ".install-monitor")); err != nil {
		t.Fatal(err)
	}
	second := readInstallMonitorTokenHTTP(t, sc)
	if second != first {
		t.Fatal("移除本地文件后应从数据库读到同一个令牌（多副本场景）")
	}
}
