package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/setting"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestSyncExternalURLToAgents 验证自动同步只更新「未手工覆盖」或「上次自动同步」的主机。
func TestSyncExternalURLToAgents(t *testing.T) {
	db, sc := setupHostDB(t)
	prefix := fmt.Sprintf("extsync-%d", time.Now().UnixNano())
	insertHost := func(suffix, agentID, override string, auto bool) string {
		t.Helper()
		ip := prefix + "-" + suffix
		if _, err := db.Exec(`
			INSERT INTO hosts (ip, hostname, tenant_id, source, agent_id, server_url_override, server_url_auto)
			VALUES ($1, $2, 1, 'manual', $3, $4, $5)`,
			ip, suffix, agentID, override, auto); err != nil {
			t.Fatalf("插入测试主机失败: %v", err)
		}
		return ip
	}
	type hostState struct {
		override  string
		auto      bool
		requested bool
	}
	readState := func(ip string) hostState {
		t.Helper()
		var st hostState
		var requested sql.NullTime
		if err := db.QueryRow(`
			SELECT COALESCE(server_url_override,''), server_url_auto, tunnel_check_requested_at
			FROM hosts WHERE ip=$1`, ip).Scan(&st.override, &st.auto, &requested); err != nil {
			t.Fatalf("读取主机状态失败: %v", err)
		}
		st.requested = requested.Valid
		return st
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE ip LIKE $1`, prefix+"%") })

	manualIP := insertHost("manual", "agent-manual", "http://manual.example.com", false)
	autoIP := insertHost("auto", "agent-auto", "http://old.example.com", true)
	emptyIP := insertHost("empty", "agent-empty", "", false)
	noAgentIP := insertHost("noagent", "", "", false)

	if _, err := SyncExternalURLToAgents(context.Background(), sc, "http://new.example.com/"); err != nil {
		t.Fatalf("同步 external_url 失败: %v", err)
	}

	if st := readState(manualIP); st.override != "http://manual.example.com" || st.auto {
		t.Fatalf("手工覆盖不应被修改: %+v", st)
	}
	if st := readState(autoIP); st.override != "http://new.example.com" || !st.auto || !st.requested {
		t.Fatalf("自动同步主机应更新并触发自检: %+v", st)
	}
	if st := readState(emptyIP); st.override != "http://new.example.com" || !st.auto || !st.requested {
		t.Fatalf("未设置覆盖的主机应自动写入: %+v", st)
	}
	if st := readState(noAgentIP); st.override != "" || st.auto {
		t.Fatalf("未安装 Agent 的主机不应被写入: %+v", st)
	}

	// 运维手工修改连接地址后，该主机应转为手工覆盖，不再随 external_url 自动更新。
	var autoHostID int64
	if err := db.QueryRow(`SELECT id FROM hosts WHERE ip=$1`, autoIP).Scan(&autoHostID); err != nil {
		t.Fatal(err)
	}
	rec := callHostHandler(t, HostUpdate(sc), http.MethodPut, strconv.FormatInt(autoHostID, 10),
		`{"server_url_override":"http://manual2.example.com"}`, hostTestUser(1))
	if rec.Code != http.StatusOK {
		t.Fatalf("手工修改连接地址失败 %d: %s", rec.Code, rec.Body.String())
	}
	if st := readState(autoIP); st.override != "http://manual2.example.com" || st.auto {
		t.Fatalf("手工修改后应转为手工覆盖: %+v", st)
	}

	// external_url 清空：只清理仍由自动同步管理的主机。
	if _, err := SyncExternalURLToAgents(context.Background(), sc, ""); err != nil {
		t.Fatalf("清空 external_url 同步失败: %v", err)
	}
	if st := readState(autoIP); st.override != "http://manual2.example.com" || st.auto {
		t.Fatalf("手工覆盖不应被清空: %+v", st)
	}
	if st := readState(emptyIP); st.override != "" || st.auto || st.requested {
		t.Fatalf("自动同步覆盖应被清空并回退默认地址: %+v", st)
	}
}

// TestMaybeSyncExternalURLToAgentsGate 验证只在开启开关且地址变化（或刚开启）时下发。
func TestMaybeSyncExternalURLToAgentsGate(t *testing.T) {
	db, _ := setupHostDB(t)
	prefix := fmt.Sprintf("extgate-%d", time.Now().UnixNano())
	ip := prefix + "-host"
	if _, err := db.Exec(`
		INSERT INTO hosts (ip, hostname, tenant_id, source, agent_id)
		VALUES ($1, 'gate', 1, 'manual', 'agent-gate')`, ip); err != nil {
		t.Fatalf("插入测试主机失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE ip LIKE $1`, prefix+"%") })

	sc := &svc.ServiceContext{DB: db, Settings: setting.NewService(db, config.Default())}
	readOverride := func() (string, bool) {
		var override string
		var auto bool
		if err := db.QueryRow(`SELECT COALESCE(server_url_override,''), server_url_auto FROM hosts WHERE ip=$1`, ip).
			Scan(&override, &auto); err != nil {
			t.Fatal(err)
		}
		return override, auto
	}

	// 未开启自动同步：地址变化也不下发。
	maybeSyncExternalURLToAgents(context.Background(), sc, false, false,
		"http://old.example.com", "http://new.example.com")
	if override, auto := readOverride(); override != "" || auto {
		t.Fatalf("未开启开关时不应下发: %q auto=%v", override, auto)
	}

	// 刚开启开关：即使地址没变也下发一次。
	maybeSyncExternalURLToAgents(context.Background(), sc, false, true,
		"http://new.example.com", "http://new.example.com")
	if override, auto := readOverride(); override != "http://new.example.com" || !auto {
		t.Fatalf("开启开关后应下发: %q auto=%v", override, auto)
	}

	// 已开启且地址未变：不重复下发（先手工改回空并置 false 后验证不被改回）。
	if _, err := db.Exec(`UPDATE hosts SET server_url_override='', server_url_auto=FALSE WHERE ip=$1`, ip); err != nil {
		t.Fatal(err)
	}
	maybeSyncExternalURLToAgents(context.Background(), sc, true, true,
		"http://new.example.com", "http://new.example.com")
	if override, auto := readOverride(); override != "" || auto {
		t.Fatalf("地址未变时不应重复下发: %q auto=%v", override, auto)
	}
}

// TestUpdateSettingsAutoSyncExternalURL 验证设置页保存 external_url 后立即写入 Agent 覆盖。
func TestUpdateSettingsAutoSyncExternalURL(t *testing.T) {
	db, _ := setupHostDB(t)
	prefix := fmt.Sprintf("extsave-%d", time.Now().UnixNano())
	ip := prefix + "-host"
	if _, err := db.Exec(`
		INSERT INTO hosts (ip, hostname, tenant_id, source, agent_id)
		VALUES ($1, 'save', 1, 'manual', 'agent-save')`, ip); err != nil {
		t.Fatalf("插入测试主机失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE ip LIKE $1`, prefix+"%") })

	sc := &svc.ServiceContext{DB: db, Settings: setting.NewService(db, config.Default())}
	req := httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"server.external_url":"http://new.example.com","server.auto_sync_agent_url":"true"}`))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	UpdateSettings(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存设置失败 %d: %s", rec.Code, rec.Body.String())
	}

	var override string
	var auto bool
	if err := db.QueryRow(`
		SELECT COALESCE(server_url_override,''), server_url_auto FROM hosts WHERE ip=$1`, ip).
		Scan(&override, &auto); err != nil {
		t.Fatal(err)
	}
	if override != "http://new.example.com" || !auto {
		t.Fatalf("设置页保存后应自动下发: %q auto=%v", override, auto)
	}
}
