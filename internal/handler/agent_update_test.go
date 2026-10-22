package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/setting"
)

// TestMarkSelfUpdateSuccess 验证新 Agent 心跳能按目标 SHA256 确认自升级成功，
// 而不依赖 Agent 在 exit(75) 前上报 success。
func TestMarkSelfUpdateSuccess(t *testing.T) {
	db, _ := setupHostDB(t)
	agentID := fmt.Sprintf("agent-success-test-%d", time.Now().UnixNano())
	ip := fmt.Sprintf("success-test-%d", time.Now().UnixNano())
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var hostID int64
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, agent_id, maintenance_until, maintenance_reason, agent_status, tenant_id, source)
		VALUES ($1,'success-test',$2,NOW()+INTERVAL '10 minutes','upgrading','upgrading',1,'agent')
		RETURNING id`, ip, agentID).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM commands WHERE agent_id=$1`, agentID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	var cmdID int64
	if err := db.QueryRow(`
		INSERT INTO commands (host_id, agent_id, type, payload, status)
		VALUES ($1,$2,'self_update',$3::jsonb,'accepted') RETURNING id`,
		hostID, agentID, fmt.Sprintf(`{"to_version":"v2","artifact":{"sha256":%q}}`, hash)).Scan(&cmdID); err != nil {
		t.Fatal(err)
	}

	if n := markSelfUpdateSuccess(context.Background(), db, agentID, hash); n != 1 {
		t.Fatalf("应确认 1 条自升级指令，实际 %d", n)
	}
	var status string
	var finished bool
	if err := db.QueryRow(`SELECT status, finished_at IS NOT NULL FROM commands WHERE id=$1`, cmdID).
		Scan(&status, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "success" || !finished {
		t.Fatalf("指令应为 success 且已结束: status=%s finished=%v", status, finished)
	}
	var maintenance bool
	var agentStatus string
	if err := db.QueryRow(`SELECT maintenance_until IS NOT NULL, agent_status FROM hosts WHERE id=$1`, hostID).
		Scan(&maintenance, &agentStatus); err != nil {
		t.Fatal(err)
	}
	if maintenance || agentStatus != "online" {
		t.Fatalf("维护窗口应清除且 Agent 在线: maintenance=%v status=%s", maintenance, agentStatus)
	}

	// 哈希不匹配时不能误判成功。
	if n := markSelfUpdateSuccess(context.Background(), db, agentID, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"); n != 0 {
		t.Fatalf("哈希不同不应确认成功，实际 %d", n)
	}
}

// TestAgentSelfUpdateAPI 覆盖管理员下发接口与状态查询的 SQL/响应格式。
func TestAgentSelfUpdateAPI(t *testing.T) {
	db, sc := setupHostDB(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "upgrade-agent-linux-amd64"), []byte("api-test-agent"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Server.AgentBinaryDir = dir
	sc.Config = cfg
	sc.Settings = setting.NewService(db, cfg)
	sc.Version = "v-api-test"

	agentID := fmt.Sprintf("agent-api-test-%d", time.Now().UnixNano())
	ip := fmt.Sprintf("api-test-%d", time.Now().UnixNano())
	var hostID int64
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, agent_id, arch, kernel, tenant_id, source)
		VALUES ($1,'api-test',$2,'amd64','5.10.0',1,'agent') RETURNING id`,
		ip, agentID).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM commands WHERE agent_id=$1`, agentID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	user := &auth.User{ID: "1", Username: "admin", IsAdmin: true, TenantID: 1,
		Roles: []string{auth.RoleSuperAdmin}}
	req := httptest.NewRequest(http.MethodPost, "/api/agents/self-update",
		strings.NewReader(fmt.Sprintf(`{"host_ids":[%d]}`, hostID)))
	req = req.WithContext(auth.WithUser(req.Context(), user))
	rec := httptest.NewRecorder()
	AgentSelfUpdate(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("下发接口状态码 %d: %s", rec.Code, rec.Body.String())
	}
	var post struct {
		Code int `json:"code"`
		Data struct {
			Enqueued      int    `json:"enqueued"`
			TargetVersion string `json:"target_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &post); err != nil {
		t.Fatal(err)
	}
	if post.Code != 0 || post.Data.Enqueued != 1 || post.Data.TargetVersion != "v-api-test" {
		t.Fatalf("下发响应不符: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/agents/self-update", nil)
	req = req.WithContext(auth.WithUser(req.Context(), user))
	rec = httptest.NewRecorder()
	AgentSelfUpdateStatus(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态接口状态码 %d: %s", rec.Code, rec.Body.String())
	}
	var status struct {
		Code int `json:"code"`
		Data struct {
			Version    string           `json:"version"`
			Binaries   []map[string]any `json:"binaries"`
			Recent     []map[string]any `json:"recent"`
			AutoUpdate bool             `json:"auto_update"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range status.Data.Recent {
		if it["agent_id"] == agentID {
			found = true
			break
		}
	}
	if status.Code != 0 || status.Data.Version != "v-api-test" || len(status.Data.Binaries) != 1 ||
		!found || status.Data.AutoUpdate {
		t.Fatalf("状态响应不符: %s", rec.Body.String())
	}
}
