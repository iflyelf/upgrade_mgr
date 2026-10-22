package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/agent/agentrun"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/handler"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestUpgradeClosedLoop 端到端：创建组件→上传制品→注册 Agent→建任务→启动→
// Agent 长轮询取件→执行替换→上报，最终校验目标文件被替换为新版本内容。
func TestUpgradeClosedLoop(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过端到端测试")
	}
	os.Setenv("AUTH_PROVIDER", "builtin")
	os.Setenv("ADMIN_PASSWORD", "admin123")
	os.Setenv("SESSION_SECRET", "e2e-test-secret-at-least-16bytes")
	os.Setenv("ARTIFACT_DIR", t.TempDir())

	c, _ := config.Load("")
	c.Agent.PollHold = 3
	c.Agent.RequireEnrollToken = false // E2E 测试环境关闭注册令牌（简化 Agent 模拟）
	sc := svc.New(c)

	// 启动灰度编排器（按批次推进任务）
	orchCtx, orchCancel := context.WithCancel(context.Background())
	defer orchCancel()
	sc.StartOrchestrator(orchCtx)

	mux := http.NewServeMux()
	registerRoutes(mux, sc)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 清理可能的历史数据
	// 按外键依赖顺序清理历史残留：upgrade_tasks 以 ON DELETE RESTRICT 引用
	// components，必须先删任务（及其级联的 task_hosts/commands）再删组件，
	// 否则组件删除会静默失败，导致下次运行因 name 唯一约束冲突而失败。
	sc.DB.Exec(`DELETE FROM upgrade_tasks WHERE component_id IN (SELECT id FROM components WHERE name='e2e_demo')`)
	sc.DB.Exec(`DELETE FROM components WHERE name='e2e_demo'`)

	admin := loginClient(t, ts)

	// 1) 目标文件（模拟被升级的二进制），初始内容 v1
	workDir := t.TempDir()
	installPath := filepath.Join(workDir, "e2e_demo_bin")
	os.WriteFile(installPath, []byte("OLD-VERSION"), 0755)

	// 2) 创建组件（replace 模式，无服务，无健康检查以简化）
	compBody := map[string]any{
		"name":         "e2e_demo",
		"display_name": "E2E Demo",
		"deploy_mode":  "replace",
		"install_path": installPath,
		"backup_dir":   filepath.Join(workDir, "backup"),
		"service_mgr":  "none",
	}
	var compResp struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	doJSON(t, admin, "POST", ts.URL+"/api/components", compBody, &compResp)
	compID := compResp.Data.ID
	if compID == 0 {
		t.Fatal("组件创建失败")
	}
	defer sc.DB.Exec(`DELETE FROM components WHERE id=$1`, compID)

	// 3) 上传制品（新版本内容 NEW-VERSION-v2，arch=当前）
	arch := "amd64"
	uploadArtifact(t, admin, ts.URL+"/api/artifacts", compID, "2.0.0", arch, "NEW-VERSION-v2")

	// 4) 注册 Agent（模拟本机，arch 对齐）
	agentIP := fmt.Sprintf("10.99.0.%d", time.Now().Unix()%250+1)
	regBody := map[string]any{
		"hostname": "e2e-host", "ip": agentIP, "os": "linux", "arch": arch,
		"agent_version": "0.1.0", "supervisor": "none",
	}
	agentID, agentToken, agentSecret := doAgentRegister(t, sc, agentIP, regBody)
	defer sc.DB.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID)
	defer sc.DB.Exec(`DELETE FROM hosts WHERE ip=$1`, agentIP)

	// Agent 注册后 source 必须变为 agent，避免被 Consul 自动清理误删。
	var hostSource string
	if err := sc.DB.QueryRow(`SELECT source FROM hosts WHERE ip=$1`, agentIP).Scan(&hostSource); err != nil {
		t.Fatalf("读取 Agent 主机 source 失败: %v", err)
	}
	if hostSource != "agent" {
		t.Fatalf("Agent 注册后 source 应为 agent，实际 %q", hostSource)
	}

	// 5) 创建任务（selector 精确命中该 IP）
	taskBody := map[string]any{
		"name": "e2e task", "component_id": compID, "version": "2.0.0",
		"selector": map[string]any{"ip_expr": agentIP},
		"strategy": map[string]any{"mode": "count", "batch_size": 1, "auto_promote": true},
	}
	var taskResp struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	doJSON(t, admin, "POST", ts.URL+"/api/tasks", taskBody, &taskResp)
	taskID := taskResp.Data.ID
	if taskID == 0 {
		t.Fatal("任务创建失败")
	}
	defer sc.DB.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)

	// 6) 启动任务（生成 command）
	doJSON(t, admin, "POST", fmt.Sprintf("%s/api/tasks/%d/start", ts.URL, taskID), nil, nil)

	// 7) 启动 Agent 主循环（用已有凭据，跳过 register）
	agentCfg := agentrun.Config{
		ServerURL: ts.URL,
		AgentID:   agentID,
		Token:     agentToken,
		Secret:    agentSecret,
	}
	ag := agentrun.New(agentCfg)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	go ag.Run(ctx)

	// 8) 轮询等待目标文件被替换为新版本
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(installPath)
		if string(data) == "NEW-VERSION-v2" {
			t.Logf("✅ 升级成功：目标文件已替换为新版本")
			// 校验 task_hosts 状态
			var status string
			sc.DB.QueryRow(`SELECT status FROM task_hosts WHERE task_id=$1`, taskID).Scan(&status)
			t.Logf("task_host 状态: %s", status)
			if status != "success" {
				t.Errorf("期望 task_host=success, 实际=%s", status)
			}
			cancel()
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatal("超时：目标文件未被替换")
}

// ---- 测试辅助 ----

func loginClient(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	jar := &cookieJar{}
	client := &http.Client{Jar: jar}
	doJSON(t, client, "POST", ts.URL+"/api/auth/login",
		map[string]any{"username": "admin", "password": "admin123"}, nil)
	return client
}

// doAgentRegister 直接调用 handler 注册 Agent，显式设置 RemoteAddr 为指定 IP。
//
// 背景：为防止主机劫持，AgentRegister 已改为从 TCP 连接来源取 IP，不再信任请求体。
// e2e 测试需要 Agent 注册到特定 IP（10.99.0.x）以便任务选择器命中，
// 而 httptest.Server 的 HTTP 调用 RemoteAddr 固定为 127.0.0.1，故直接调用 handler。
func doAgentRegister(t *testing.T, sc *svc.ServiceContext, ip string, body map[string]any) (agentID, token, secret string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/agent/register", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":12345" // 显式设置来源 IP
	rec := httptest.NewRecorder()
	handler.AgentRegister(sc)(rec, req)
	if rec.Code != 200 {
		t.Fatalf("Agent 注册失败 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			AgentID string `json:"agent_id"`
			Token   string `json:"token"`
			Secret  string `json:"secret"`
		} `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.AgentID == "" || resp.Data.Secret == "" {
		t.Fatalf("Agent 注册响应缺少凭据: %s", rec.Body.String())
	}
	var dbSecret string
	if err := sc.DB.QueryRow(`SELECT secret FROM agents WHERE agent_id=$1`, resp.Data.AgentID).Scan(&dbSecret); err != nil {
		t.Fatalf("读取 Agent 密钥失败: %v", err)
	}
	if dbSecret != resp.Data.Secret {
		t.Fatalf("Agent 密钥不一致：响应=%q 数据库=%q", resp.Data.Secret, dbSecret)
	}
	return resp.Data.AgentID, resp.Data.Token, resp.Data.Secret
}

func doJSON(t *testing.T, c *http.Client, method, url string, body, out any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rdr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("%s %s -> %d: %s", method, url, resp.StatusCode, rb)
	}
	if out != nil {
		if err := json.Unmarshal(rb, out); err != nil {
			t.Fatalf("解析响应失败: %v (%s)", err, rb)
		}
	}
}

func uploadArtifact(t *testing.T, c *http.Client, url string, compID int64, version, arch, content string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("component_id", fmt.Sprintf("%d", compID))
	mw.WriteField("version", version)
	mw.WriteField("arch", arch)
	fw, _ := mw.CreateFormFile("file", "demo.bin")
	fw.Write([]byte(content))
	mw.Close()

	req, _ := http.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("upload -> %d: %s", resp.StatusCode, rb)
	}
}
