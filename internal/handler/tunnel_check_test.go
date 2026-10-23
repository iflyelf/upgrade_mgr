package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/middleware"
)

// insertHeartbeatAgent 插入一个最小 Agent + 主机，返回 agentID/token。
func insertHeartbeatAgent(t *testing.T, agentID, token, tunnelURL string) int64 {
	t.Helper()
	db, sc := setupHostDB(t)
	_ = sc
	ip := fmt.Sprintf("hb-tunnel-%d", time.Now().UnixNano())
	var hostID int64
	if _, err := db.Exec(`
		INSERT INTO agents (agent_id, ip, token_hash, secret, status)
		VALUES ($1, $2, $3, 'secret', 'online')`,
		agentID, ip, agentgw.HashToken(token)); err != nil {
		t.Fatalf("插入 Agent 失败: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, tenant_id, source, agent_id, server_url_override)
		VALUES ($1, 'hb', 1, 'agent', $2, $3) RETURNING id`,
		ip, agentID, tunnelURL).Scan(&hostID); err != nil {
		t.Fatalf("插入主机失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
		db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID)
	})
	return hostID
}

// TestHeartbeatRequestsTunnelCheck 保存隧道后心跳应下发 tunnel_check_url，
// Agent 回报结果后写入 hosts 并清除待检查标记。
func TestHeartbeatRequestsTunnelCheck(t *testing.T) {
	agentID := fmt.Sprintf("hb-tunnel-%d", time.Now().UnixNano())
	token := "hb-tunnel-token"
	hostID := insertHeartbeatAgent(t, agentID, token, "10.8.0.5:8090")

	db, sc := setupHostDB(t)
	sc.Config = config.Default()
	sc.AgentGW = agentgw.New(db)

	// 模拟保存隧道时的初始化。
	triggerTunnelCheck(sc, hostID)

	// 第一次心跳：应下发待测地址。
	req := httptest.NewRequest("POST", "/api/agent/heartbeat",
		strings.NewReader(`{"status":"online","agent_default_server_url":"http://10.8.0.5:8090"}`))
	req.Header.Set("Authorization", "Bearer "+agentID+":"+token)
	rec := httptest.NewRecorder()
	middleware.RequireAgentAuth(sc)(http.HandlerFunc(AgentHeartbeat(sc))).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("心跳失败 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			ServerURL      string `json:"server_url"`
			TunnelCheckURL string `json:"tunnel_check_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if resp.Data.ServerURL != "http://10.8.0.5:8090" {
		t.Fatalf("应下发隧道地址，got %q", resp.Data.ServerURL)
	}
	if resp.Data.TunnelCheckURL == "" {
		t.Fatal("应要求主机侧自检")
	}

	// 第二次心跳：带上自检结果，服务端应写入并清除待检查标记。
	body := `{"status":"online","agent_default_server_url":"http://10.8.0.5:8090",` +
		`"tunnel_check":{"url":"http://10.8.0.5:8090","ok":false,"latency_ms":42,"detail":"TCP 连接失败"}}`
	req = httptest.NewRequest("POST", "/api/agent/heartbeat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+agentID+":"+token)
	rec = httptest.NewRecorder()
	middleware.RequireAgentAuth(sc)(http.HandlerFunc(AgentHeartbeat(sc))).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("心跳失败 %d: %s", rec.Code, rec.Body.String())
	}

	var (
		checkOK  *bool
		source   string
		detail   string
		latency  int
		requestd *time.Time
	)
	if err := db.QueryRow(`
		SELECT tunnel_check_ok, COALESCE(tunnel_check_source,''), COALESCE(tunnel_check_detail,''),
		       COALESCE(tunnel_check_latency_ms,0), tunnel_check_requested_at
		FROM hosts WHERE id=$1`, hostID).
		Scan(&checkOK, &source, &detail, &latency, &requestd); err != nil {
		t.Fatal(err)
	}
	if checkOK == nil || *checkOK {
		t.Fatalf("应记录主机侧自检失败: %v", checkOK)
	}
	if source != "agent" || latency != 42 || !strings.Contains(detail, "TCP") {
		t.Fatalf("主机侧结果不符: source=%q latency=%d detail=%q", source, latency, detail)
	}
	if requestd != nil {
		t.Fatal("回报结果后应清除待检查标记")
	}
}
