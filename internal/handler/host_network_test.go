package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestHostUpdateTunnelIP 验证隧道地址可保存、可清空，并拒绝非法值。
func TestHostUpdateTunnelIP(t *testing.T) {
	db, sc := setupHostDB(t)
	hostID, _ := insertTestHost(t, db, 1)
	idStr := strconv.FormatInt(hostID, 10)

	rec := callHostHandler(t, HostUpdate(sc), http.MethodPut, idStr,
		`{"server_tunnel_url":"10.8.0.5:8090","tunnel_ip":"172.16.0.9"}`, hostTestUser(1))
	if rec.Code != http.StatusOK {
		t.Fatalf("保存隧道地址失败 %d: %s", rec.Code, rec.Body.String())
	}
	var serverTunnel, hostTunnel string
	if err := db.QueryRow(`SELECT server_tunnel_url, tunnel_ip FROM hosts WHERE id=$1`, hostID).
		Scan(&serverTunnel, &hostTunnel); err != nil {
		t.Fatal(err)
	}
	if serverTunnel != "10.8.0.5:8090" || hostTunnel != "172.16.0.9" {
		t.Fatalf("server_tunnel_url=%q tunnel_ip=%q", serverTunnel, hostTunnel)
	}

	// 非法协议应 400，且不写入
	rec = callHostHandler(t, HostUpdate(sc), http.MethodPut, idStr,
		`{"server_tunnel_url":"ftp://bad.example.com"}`, hostTestUser(1))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法隧道地址应 400，实际 %d", rec.Code)
	}
	db.QueryRow(`SELECT server_tunnel_url FROM hosts WHERE id=$1`, hostID).Scan(&serverTunnel)
	if serverTunnel != "10.8.0.5:8090" {
		t.Fatalf("非法值不应覆盖原值: %q", serverTunnel)
	}

	// 主机侧隧道 IP 只接受 IP 字面量
	rec = callHostHandler(t, HostUpdate(sc), http.MethodPut, idStr,
		`{"tunnel_ip":"not-an-ip"}`, hostTestUser(1))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法主机侧隧道 IP 应 400，实际 %d", rec.Code)
	}

	// 显式空串表示清空隧道
	rec = callHostHandler(t, HostUpdate(sc), http.MethodPut, idStr,
		`{"server_tunnel_url":"","tunnel_ip":""}`, hostTestUser(1))
	if rec.Code != http.StatusOK {
		t.Fatalf("清空隧道失败 %d: %s", rec.Code, rec.Body.String())
	}
	db.QueryRow(`SELECT server_tunnel_url, tunnel_ip FROM hosts WHERE id=$1`, hostID).
		Scan(&serverTunnel, &hostTunnel)
	if serverTunnel != "" || hostTunnel != "" {
		t.Fatalf("隧道地址应被清空: %q %q", serverTunnel, hostTunnel)
	}
}

// TestTunnelServerURLFillsDefaults 隧道只填 IP 时，按 Agent 上报的默认地址补协议与端口。
func TestTunnelServerURLFillsDefaults(t *testing.T) {
	sc := &svc.ServiceContext{}
	req := httptest.NewRequest("POST", "/api/agent/heartbeat", nil)

	if got := tunnelServerURL(sc, req, "", "http://10.0.0.5:8090"); got != "" {
		t.Fatalf("未配置隧道应返回空串，got %q", got)
	}
	if got := tunnelServerURL(sc, req, "10.8.0.5", "http://10.0.0.5:8090"); got != "http://10.8.0.5:8090" {
		t.Fatalf("应沿用默认端口，got %q", got)
	}
	if got := tunnelServerURL(sc, req, "10.8.0.5:9000", "http://10.0.0.5:8090"); got != "http://10.8.0.5:9000" {
		t.Fatalf("应沿用默认协议，got %q", got)
	}
	if got := tunnelServerURL(sc, req, "https://tunnel.example.com", "http://10.0.0.5:8090"); got != "https://tunnel.example.com" {
		t.Fatalf("完整 URL 应原样下发，got %q", got)
	}
}

// TestListHostsIncludesNetworkFields 主机列表返回出口 IP / 隧道地址 / 当前连接地址。
func TestListHostsIncludesNetworkFields(t *testing.T) {
	db, sc := setupHostDB(t)
	ip := fmt.Sprintf("net-test-%d", time.Now().UnixNano())
	var id int64
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, tenant_id, source, exit_ip, tunnel_ip,
		                   server_tunnel_url, agent_server_url, agent_default_server_url)
		VALUES ($1, 'net-test', 1, 'manual', '192.0.2.10', '172.16.0.9',
		        '10.8.0.5:8090', 'http://10.8.0.5:8090', 'http://192.0.2.10:8090')
		RETURNING id`, ip).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE id=$1`, id) })

	// 出口 IP 也应能作为关键字搜到
	req := httptest.NewRequest("GET", "/api/hosts?q=192.0.2.10", nil)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	ListHosts(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表查询失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			List []Host `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	var found *Host
	for i := range out.Data.List {
		if out.Data.List[i].ID == id {
			found = &out.Data.List[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("未按出口 IP 搜到主机: %s", rec.Body.String())
	}
	if found.ExitIP != "192.0.2.10" || found.TunnelIP != "172.16.0.9" ||
		found.ServerTunnelURL != "10.8.0.5:8090" {
		t.Fatalf("网络字段未返回: %+v", found)
	}
	if found.AgentServerURL != "http://10.8.0.5:8090" || found.DefaultServerURL != "http://192.0.2.10:8090" {
		t.Fatalf("连接地址字段未返回: %+v", found)
	}
}

// TestHostTunnelTestProbesAddress 验证拨测接口：可达的平台地址返回成功与阶段明细。
func TestHostTunnelTestProbesAddress(t *testing.T) {
	db, sc := setupHostDB(t)

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","service":"upgrade_mgr"}`))
	}))
	defer health.Close()

	ip := fmt.Sprintf("tunnel-test-%d", time.Now().UnixNano())
	var id int64
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, tenant_id, source, tunnel_ip, server_tunnel_url,
		                   agent_default_server_url)
		VALUES ($1, 'tunnel-test', 1, 'manual', '172.16.0.9', $2, $2)
		RETURNING id`, ip, health.URL).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE id=$1`, id) })

	req := httptest.NewRequest("POST", "/api/hosts/"+strconv.FormatInt(id, 10)+"/tunnel-test",
		strings.NewReader(`{}`))
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	HostTunnelTest(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("拨测失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			TunnelIP string `json:"tunnel_ip"`
			Result   struct {
				OK     bool `json:"ok"`
				Stages []struct {
					Name string `json:"name"`
					OK   bool   `json:"ok"`
				} `json:"stages"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if !out.Data.Result.OK {
		t.Fatalf("平台侧地址应可达: %s", rec.Body.String())
	}
	if out.Data.TunnelIP != "172.16.0.9" || len(out.Data.Result.Stages) == 0 {
		t.Fatalf("响应缺少隧道信息: %+v", out.Data)
	}
}

// TestHostTunnelTestRejectsUnconfigured 未配置隧道地址时应提示，而不是空拨测。
func TestHostTunnelTestRejectsUnconfigured(t *testing.T) {
	db, sc := setupHostDB(t)
	hostID, _ := insertTestHost(t, db, 1)
	idStr := strconv.FormatInt(hostID, 10)

	req := httptest.NewRequest("POST", "/api/hosts/"+idStr+"/tunnel-test", strings.NewReader(`{}`))
	req.SetPathValue("id", idStr)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	HostTunnelTest(sc)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未配置隧道应 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHostTunnelTestRejectsOtherTenant 跨租户拨测应 404。
func TestHostTunnelTestRejectsOtherTenant(t *testing.T) {
	db, sc := setupHostDB(t)
	hostID, _ := insertTestHost(t, db, 1)
	idStr := strconv.FormatInt(hostID, 10)

	req := httptest.NewRequest("POST", "/api/hosts/"+idStr+"/tunnel-test", strings.NewReader(`{}`))
	req.SetPathValue("id", idStr)
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(9902)))
	rec := httptest.NewRecorder()
	HostTunnelTest(sc)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨租户应 404，实际 %d", rec.Code)
	}
}
