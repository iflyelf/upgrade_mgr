package handler

import (
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestAgentStats 版本/OS/架构分布、离线与过期统计。
func TestAgentStats(t *testing.T) {
	db, _ := setupHostDB(t)
	sfx := time.Now().UnixNano()
	sc := &svc.ServiceContext{
		DB:      db,
		Version: "1.0.0",
		// Settings 为 nil 时 AgentStats 使用默认的 1m 离线阈值
	}

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

// TestAgentStatsUsesBuildVersionAndTargetHash 验证版本分布：
//  1. 优先使用 build_version，历史 agent_version 仅作为兜底；
//  2. 是否最新按目标二进制 SHA256 判断，而不是只比较版本字符串。
func TestAgentStatsUsesBuildVersionAndTargetHash(t *testing.T) {
	db, _ := setupHostDB(t)
	sfx := time.Now().UnixNano()

	dir := t.TempDir()
	targetData := []byte("target-agent-binary-for-stats")
	if err := os.WriteFile(filepath.Join(dir, "upgrade-agent-linux-amd64"), targetData, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(targetData)
	targetHash := hex.EncodeToString(sum[:])

	sc := &svc.ServiceContext{DB: db, Version: "9.9.9", Config: config.Default()}
	sc.Config.Server.AgentBinaryDir = dir

	insertAgent := func(kind, version, hash string) (hostID int64, agentID string) {
		t.Helper()
		ip := fmt.Sprintf("stats-%s-%d", kind, sfx)
		if err := db.QueryRow(`
			INSERT INTO hosts (ip, hostname, arch, os_family, tenant_id)
			VALUES ($1,$2,'amd64','linux',1) RETURNING id`, ip, kind).Scan(&hostID); err != nil {
			t.Fatal(err)
		}
		agentID = fmt.Sprintf("stats-%s-agent-%d", kind, sfx)
		if _, err := db.Exec(`
			INSERT INTO agents (agent_id, host_id, ip, token_hash, arch, os,
			                    agent_version, build_version, build_hash, status, last_heartbeat_at)
			VALUES ($1,$2,$3,'hash','amd64','linux','0.1.0',$4,$5,'online',NOW())`,
			agentID, hostID, ip, version, hash); err != nil {
			t.Fatal(err)
		}
		return hostID, agentID
	}

	newVersion := fmt.Sprintf("9.9.9-new-%d", sfx)
	oldVersion := fmt.Sprintf("0.1.0-old-%d", sfx)
	newHostID, newAgentID := insertAgent("new", newVersion, targetHash)
	oldHostID, oldAgentID := insertAgent("old", oldVersion, strings.Repeat("a", 64))
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agents WHERE agent_id IN ($1,$2)`, newAgentID, oldAgentID)
		db.Exec(`DELETE FROM hosts WHERE id IN ($1,$2)`, newHostID, oldHostID)
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
			Outdated      int    `json:"outdated"`
			List          []struct {
				Version       string `json:"version"`
				Count         int    `json:"count"`
				Outdated      bool   `json:"outdated"`
				OutdatedCount int    `json:"outdated_count"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.TargetVersion != "9.9.9" || out.Data.Total < 2 {
		t.Fatalf("统计头部不符: %+v", out.Data)
	}

	byVersion := map[string]bool{}
	for _, item := range out.Data.List {
		byVersion[item.Version] = true
		switch item.Version {
		case newVersion:
			if item.Count != 1 || item.Outdated || item.OutdatedCount != 0 {
				t.Fatalf("目标哈希的 Agent 应标记最新: %+v", item)
			}
		case oldVersion:
			if item.Count != 1 || !item.Outdated || item.OutdatedCount != 1 {
				t.Fatalf("旧哈希的 Agent 应标记旧版本: %+v", item)
			}
		}
	}
	if !byVersion[newVersion] || !byVersion[oldVersion] {
		t.Fatalf("未按 build_version 分组: %+v", out.Data.List)
	}
	if out.Data.Outdated < 1 {
		t.Fatalf("旧版本计数应至少为 1: %+v", out.Data)
	}
}
