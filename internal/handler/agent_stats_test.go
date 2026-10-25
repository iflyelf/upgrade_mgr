package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestAgentStats 版本/OS/架构分布、离线与过期统计。
func TestAgentStats(t *testing.T) {
	db, _ := setupHostDB(t)
	sfx := time.Now().UnixNano()
	sc := &svc.ServiceContext{DB: db, Version: "1.0.0"}

	var hostID int64
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, arch, os_family, tenant_id)
		VALUES ($1,'stats-host','amd64','linux',1) RETURNING id`,
		fmt.Sprintf("10.250.%d.%d", (sfx/256)%256, sfx%256)).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	agentID := fmt.Sprintf("stats-agent-%d", sfx)
	// 测试库可能被同包其它测试写入过数据，统计是全表聚合的：
	// 使用唯一版本号定位本用例的 Agent，避免断言被历史数据污染。
	agentVersion := fmt.Sprintf("0.9.0-stats-%d", sfx)
	if _, err := db.Exec(`
		INSERT INTO agents (agent_id, host_id, ip, token_hash, arch, os, agent_version,
		                    status, last_heartbeat_at)
		VALUES ($1,$2,'10.0.0.1','hash','amd64','linux',$3,'offline',NOW() - INTERVAL '1 hour')`,
		agentID, hostID, agentVersion); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	req := httptest.NewRequest("GET", "/api/agents/stats", nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	AgentStats(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("查询失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			TargetVersion string `json:"target_version"`
			Total         int    `json:"total"`
			Offline       int    `json:"offline"`
			Outdated      int    `json:"outdated"`
			List          []struct {
				Version  string `json:"version"`
				Count    int    `json:"count"`
				Offline  int    `json:"offline"`
				Outdated bool   `json:"outdated"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.TargetVersion != "1.0.0" || out.Data.Total < 1 ||
		out.Data.Offline < 1 || out.Data.Outdated < 1 {
		t.Fatalf("统计不符: %+v", out.Data)
	}
	found := false
	for _, item := range out.Data.List {
		if item.Version != agentVersion {
			continue
		}
		found = true
		if item.Count != 1 || item.Offline != 1 || !item.Outdated {
			t.Fatalf("版本分布不符: %+v", item)
		}
	}
	if !found {
		t.Fatalf("未找到本用例的 Agent 版本 %q: %+v", agentVersion, out.Data.List)
	}
}
