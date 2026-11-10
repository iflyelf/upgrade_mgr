package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/middleware"
	"github.com/iflyelf/upgrade_mgr/internal/setting"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

func TestClientIPUsesTrustedProxy(t *testing.T) {
	cfg := config.Default()
	cfg.Server.TrustProxy = true
	cfg.Server.TrustedProxies = []string{"127.0.0.1"}
	sc := &svc.ServiceContext{Config: cfg}

	req := httptest.NewRequest("POST", "/api/agent/register", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := clientIP(sc, req); got != "203.0.113.9" {
		t.Fatalf("受信代理的 XFF 应被采用，got %q", got)
	}

	// 直连客户端即使伪造 XFF，也不应被信任。
	req.RemoteAddr = "198.51.100.7:12345"
	if got := clientIP(sc, req); got != "198.51.100.7" {
		t.Fatalf("非受信来源应忽略 XFF，got %q", got)
	}
}

func TestBuildInstallCommandRemovesStaleBinary(t *testing.T) {
	cmd := buildInstallCommand("http://10.1.230.50:8090", "amd64", "tok-123", false, "")

	// 验证基本结构
	if !strings.Contains(cmd, "KERNEL=$(uname -r)") {
		t.Fatalf("安装命令应先获取内核版本，got:\n%s", cmd)
	}
	if !strings.Contains(cmd, "rm -f /tmp/upgrade-agent") {
		t.Fatalf("安装命令必须先删除旧二进制，got:\n%s", cmd)
	}
	// 验证 URL 使用双引号以便变量展开
	if !strings.Contains(cmd, `"http://10.1.230.50:8090/api/agent/binary/linux-amd64?kernel=${KERNEL}"`) {
		t.Fatalf("安装命令应使用双引号包裹 URL 并引用 KERNEL 变量，got:\n%s", cmd)
	}
	for _, want := range []string{
		"wget -c",
		"chmod +x /tmp/upgrade-agent",
		"install --server 'http://10.1.230.50:8090' --enroll-token 'tok-123'",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("安装命令缺少 %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "--force") {
		t.Fatalf("普通安装不应带 --force:\n%s", cmd)
	}
}

func TestBuildWindowsInstallCommand(t *testing.T) {
	cmd := buildWindowsInstallCommand("http://10.1.230.50:8090", "amd64", "tok-123", false)
	for _, want := range []string{
		"System.Net.WebClient",
		"/api/agent/binary/windows-amd64",
		"Join-Path $env:TEMP 'upgrade-agent.exe'",
		"'install'",
		"'--enroll-token'",
		"'tok-123'",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("Windows 安装命令缺少 %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "Invoke-WebRequest") {
		t.Fatalf("不能使用 PowerShell 3.0+ 的 Invoke-WebRequest（2008 R2 只有 PS 2.0）:\n%s", cmd)
	}
	if strings.Contains(cmd, "--force") {
		t.Fatalf("普通安装不应带 --force:\n%s", cmd)
	}
	if !strings.Contains(buildWindowsInstallCommand("http://x", "amd64", "t", true), "'--force'") {
		t.Fatal("强制注册命令应带 --force")
	}
}

func TestIsValidTarget(t *testing.T) {
	ok := [][2]string{
		{"linux", "amd64"}, {"", "386"}, {"windows", "amd64"}, {"windows", "arm64"},
		{"Linux", "arm64-legacy"},
	}
	for _, c := range ok {
		if !isValidTarget(c[0], c[1]) {
			t.Errorf("应允许 %v", c)
		}
	}
	bad := [][2]string{
		{"windows", "386"}, {"windows", "arm"}, {"windows", "amd64-legacy"},
		{"darwin", "amd64"}, {"linux", "mips"},
	}
	for _, c := range bad {
		if isValidTarget(c[0], c[1]) {
			t.Errorf("应拒绝 %v", c)
		}
	}
}

func TestInstallBaseURLPrefersExternalURL(t *testing.T) {
	r := httptest.NewRequest("GET", "http://internal:8090/api/agent/enroll-token", nil)
	r.Host = "10.1.230.50:8090"

	if got := installBaseURL(" https://upgrade.example.com/ ", r, false); got != "https://upgrade.example.com" {
		t.Fatalf("external_url 应优先且去掉结尾斜杠，got %q", got)
	}
	if got := installBaseURL("", r, false); got != "http://10.1.230.50:8090" {
		t.Fatalf("留空应回退请求 Host，got %q", got)
	}
}

func TestInstallBaseURLForwardedProtoOnlyWhenTrusted(t *testing.T) {
	r := httptest.NewRequest("GET", "http://internal:8090/api/agent/enroll-token", nil)
	r.Host = "upgrade.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")

	if got := installBaseURL("", r, false); got != "http://upgrade.example.com" {
		t.Fatalf("trust_proxy=false 不应采信 X-Forwarded-Proto，got %q", got)
	}
	if got := installBaseURL("", r, true); got != "https://upgrade.example.com" {
		t.Fatalf("trust_proxy=true 应采信 X-Forwarded-Proto，got %q", got)
	}
}

func TestInstallBaseURLTLS(t *testing.T) {
	r := httptest.NewRequest("GET", "https://upgrade.example.com/api/agent/enroll-token", nil)
	if got := installBaseURL("", r, false); got != "https://upgrade.example.com" {
		t.Fatalf("TLS 请求应生成 https，got %q", got)
	}
}

func TestBuildInstallCommandForce(t *testing.T) {
	cmd := buildInstallCommand("http://10.1.230.50:8090", "amd64", "tok-123", true, "")
	if !strings.HasSuffix(cmd, "--force") {
		t.Fatalf("强制重新注册命令应以 --force 结尾，got:\n%s", cmd)
	}
	if !strings.Contains(cmd, "--enroll-token 'tok-123' --force") {
		t.Fatalf("--force 必须与 --enroll-token 同时出现，got:\n%s", cmd)
	}
}

func TestBuildInstallCommandLegacy(t *testing.T) {
	cmd := buildInstallCommand("http://10.1.230.50:8090", "amd64", "tok-123", false, "legacy")
	if !strings.Contains(cmd, "/api/agent/binary/linux-amd64-legacy") {
		t.Fatalf("legacy 变体应下载 linux-amd64-legacy 二进制，got:\n%s", cmd)
	}
	// 验证完整命令结构
	if !strings.Contains(cmd, "rm -f /tmp/upgrade-agent") {
		t.Fatalf("应先删除旧二进制，got:\n%s", cmd)
	}
	if !strings.Contains(cmd, "wget") || !strings.Contains(cmd, "chmod +x") {
		t.Fatalf("应包含 wget 和 chmod 步骤，got:\n%s", cmd)
	}
}

func TestAgentRegisterStoresSignatureModes(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Config = config.Default()
	sc.Config.Agent.RequireEnrollToken = false

	cases := []struct {
		name string
		body string
		ip   string
		want string
	}{
		{
			name: "上报全部能力",
			body: `{"hostname":"sig-register-all","arch":"amd64","signature_modes":["v2","unknown","signed_path","id","id"]}`,
			ip:   "203.0.113.251",
			want: "id,signed_path,v2",
		},
		{
			name: "旧 Agent 未上报",
			body: `{"hostname":"sig-register-old","arch":"amd64"}`,
			ip:   "203.0.113.252",
			want: "id",
		},
		{
			name: "未知能力回退 id",
			body: `{"hostname":"sig-register-unknown","arch":"amd64","signature_modes":["future_mode"]}`,
			ip:   "203.0.113.253",
			want: "id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/agent/register", strings.NewReader(tc.body))
			req.RemoteAddr = tc.ip + ":12345"
			rec := httptest.NewRecorder()
			AgentRegister(sc)(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("注册失败 %d: %s", rec.Code, rec.Body.String())
			}
			var out struct {
				Code int `json:"code"`
				Data struct {
					AgentID string `json:"agent_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("解析注册响应失败: %v", err)
			}
			if out.Code != 0 || out.Data.AgentID == "" {
				t.Fatalf("注册响应异常: %s", rec.Body.String())
			}
			agentID := out.Data.AgentID
			t.Cleanup(func() {
				db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID)
				db.Exec(`DELETE FROM hosts WHERE agent_id=$1`, agentID)
			})

			var got string
			if err := db.QueryRow(`SELECT signature_modes FROM agents WHERE agent_id=$1`, agentID).Scan(&got); err != nil {
				t.Fatalf("查询签名能力失败: %v", err)
			}
			if got != tc.want {
				t.Fatalf("signature_modes = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentHeartbeatUpdatesSignatureModes(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Config = config.Default()
	sc.AgentGW = agentgw.New(db)
	sc.Settings = setting.NewService(db, sc.Config)

	agentID := fmt.Sprintf("hb-sig-mode-%d", time.Now().UnixNano())
	token := "hb-sig-token"
	if _, err := db.Exec(`
		INSERT INTO agents (agent_id, ip, token_hash, secret, signature_modes, status)
		VALUES ($1, '127.0.0.1', $2, 'secret', 'id', 'online')`,
		agentID, agentgw.HashToken(token)); err != nil {
		t.Fatalf("插入 Agent 失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID) })

	req := httptest.NewRequest("POST", "/api/agent/heartbeat",
		strings.NewReader(`{"status":"online","signature_modes":["signed_path","v2"]}`))
	req.Header.Set("Authorization", "Bearer "+agentID+":"+token)
	rec := httptest.NewRecorder()
	middleware.RequireAgentAuth(sc)(http.HandlerFunc(AgentHeartbeat(sc))).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("心跳失败 %d: %s", rec.Code, rec.Body.String())
	}

	var got string
	if err := db.QueryRow(`SELECT signature_modes FROM agents WHERE agent_id=$1`, agentID).Scan(&got); err != nil {
		t.Fatalf("查询签名能力失败: %v", err)
	}
	if got != "signed_path,v2" {
		t.Fatalf("心跳后 signature_modes = %q, want %q", got, "signed_path,v2")
	}

	// 旧 Agent 心跳不带该字段，不得清空已记录的能力。
	req = httptest.NewRequest("POST", "/api/agent/heartbeat", strings.NewReader(`{"status":"online"}`))
	req.Header.Set("Authorization", "Bearer "+agentID+":"+token)
	rec = httptest.NewRecorder()
	middleware.RequireAgentAuth(sc)(http.HandlerFunc(AgentHeartbeat(sc))).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("旧格式心跳失败 %d: %s", rec.Code, rec.Body.String())
	}
	if err := db.QueryRow(`SELECT signature_modes FROM agents WHERE agent_id=$1`, agentID).Scan(&got); err != nil {
		t.Fatalf("查询签名能力失败: %v", err)
	}
	if got != "signed_path,v2" {
		t.Fatalf("旧 Agent 心跳不应覆盖能力，got %q", got)
	}
}

func TestNormalizeAgentStatus(t *testing.T) {
	cases := map[string]string{
		"":             "online",
		"online":       "online",
		"UPGRADING":    "upgrading",
		"restarting":   "restarting",
		"crash_loop":   "crash_loop",
		"offline":      "offline",
		"unknown":      "unknown",
		"hacked' OR 1": "online",
	}
	for in, want := range cases {
		if got := normalizeAgentStatus(in); got != want {
			t.Fatalf("normalizeAgentStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanAgentText(t *testing.T) {
	if got := cleanAgentText("  host-1\n", 64); got != "host-1" {
		t.Fatalf("应去除空白与控制字符，got %q", got)
	}
	long := strings.Repeat("主机", 100)
	got := cleanAgentText(long, 10)
	if len(got) > 10 {
		t.Fatalf("截断后超过上限: %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断破坏了 UTF-8: %q", got)
	}
	if got := cleanAgentText("bad\xfftext", 64); strings.ContainsRune(got, '\uFFFD') || got != "badtext" {
		t.Fatalf("非法 UTF-8 应被移除，got %q", got)
	}
}

// TestAgentHeartbeatUpdatesBuildVersion 心跳必须把构建版本同步到
// agents.agent_version / hosts.agent_version，否则版本分布会一直显示注册时的旧值。
func TestAgentHeartbeatUpdatesBuildVersion(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Config = config.Default()
	sc.AgentGW = agentgw.New(db)
	sc.Settings = setting.NewService(db, sc.Config)

	sfx := time.Now().UnixNano()
	agentID := fmt.Sprintf("hb-version-%d", sfx)
	token := fmt.Sprintf("hb-version-token-%d", sfx)
	hostIP := fmt.Sprintf("stats-hb-%d", sfx)
	var hostID int64
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, agent_id, agent_version, tenant_id)
		VALUES ($1,'hb-version',$2,'0.1.0',1) RETURNING id`, hostIP, agentID).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO agents (agent_id, host_id, ip, token_hash, secret, agent_version, status)
		VALUES ($1,$2,$3,$4,'secret','0.1.0','online')`,
		agentID, hostID, hostIP, agentgw.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	hash := strings.Repeat("b", 64)
	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/agent/heartbeat", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+agentID+":"+token)
		rec := httptest.NewRecorder()
		middleware.RequireAgentAuth(sc)(http.HandlerFunc(AgentHeartbeat(sc))).ServeHTTP(rec, req)
		return rec
	}

	rec := call(`{"status":"online","build_version":"1.2.3","build_hash":"` + hash + `","build_variant":"legacy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("心跳失败 %d: %s", rec.Code, rec.Body.String())
	}
	var gotVersion, gotBuild, gotVariant string
	if err := db.QueryRow(`SELECT agent_version, COALESCE(build_version,''), COALESCE(build_variant,'')
		FROM agents WHERE agent_id=$1`, agentID).Scan(&gotVersion, &gotBuild, &gotVariant); err != nil {
		t.Fatal(err)
	}
	if gotVersion != "1.2.3" || gotBuild != "1.2.3" || gotVariant != "legacy" {
		t.Fatalf("agents 构建信息未同步: version=%q build=%q variant=%q", gotVersion, gotBuild, gotVariant)
	}
	var gotHostVersion, gotHostBuild string
	if err := db.QueryRow(`SELECT agent_version, COALESCE(agent_build_version,'')
		FROM hosts WHERE id=$1`, hostID).Scan(&gotHostVersion, &gotHostBuild); err != nil {
		t.Fatal(err)
	}
	if gotHostVersion != "1.2.3" || gotHostBuild != "1.2.3" {
		t.Fatalf("hosts 构建信息未同步: version=%q build=%q", gotHostVersion, gotHostBuild)
	}

	// 旧 Agent 不带构建字段时，不能被空值覆盖。
	rec = call(`{"status":"online"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("旧格式心跳失败 %d: %s", rec.Code, rec.Body.String())
	}
	if err := db.QueryRow(`SELECT agent_version FROM agents WHERE agent_id=$1`, agentID).Scan(&gotVersion); err != nil {
		t.Fatal(err)
	}
	if gotVersion != "1.2.3" {
		t.Fatalf("空构建信息不应覆盖 agent_version，got %q", gotVersion)
	}
}

// TestAgentRegisterMatchesExistingHostByExitIP 再次接入时若来源 IP 与清单 IP
// 不一致，应按 Agent 上报的出口 IP 匹配存量主机并更新，而不是重复建档。
func TestAgentRegisterMatchesExistingHostByExitIP(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Config = config.Default()
	sc.Config.Agent.RequireEnrollToken = false

	sfx := time.Now().UnixNano()
	existingIP := fmt.Sprintf("198.18.%d.%d", (sfx/250)%250, sfx%250)
	srcIP := fmt.Sprintf("198.19.%d.%d", (sfx/250)%250, (sfx+7)%250)
	if existingIP == srcIP {
		srcIP = "198.19.250.250"
	}

	var hostID int64
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, tenant_id, source)
		VALUES ($1,'existing-exitip',1,'consul_mgr') RETURNING id`, existingIP).Scan(&hostID); err != nil {
		t.Fatalf("插入存量主机失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agents WHERE host_id=$1`, hostID)
		db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID)
	})

	body := `{"hostname":"existing-exitip","arch":"amd64","exit_ip":"` + existingIP + `"}`
	req := httptest.NewRequest("POST", "/api/agent/register", strings.NewReader(body))
	req.RemoteAddr = srcIP + ":12345"
	rec := httptest.NewRecorder()
	AgentRegister(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("注册失败 %d: %s", rec.Code, rec.Body.String())
	}

	var total int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM hosts WHERE ip=$1 OR exit_ip=$1`, existingIP).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("出口 IP %s 应只对应 1 台主机，实际 %d", existingIP, total)
	}
	var gotIP, gotAgentID string
	if err := db.QueryRow(`SELECT ip, COALESCE(agent_id,'') FROM hosts WHERE id=$1`, hostID).
		Scan(&gotIP, &gotAgentID); err != nil {
		t.Fatal(err)
	}
	if gotIP != existingIP {
		t.Fatalf("存量主机 ip 不应被改写: got %q", gotIP)
	}
	if gotAgentID == "" {
		t.Fatal("存量主机应绑定新注册的 Agent")
	}
	var dup int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE ip=$1`, srcIP).Scan(&dup); err != nil {
		t.Fatal(err)
	}
	if dup != 0 {
		t.Fatalf("不应按来源 IP %s 新建主机", srcIP)
	}
}

// TestAgentRegisterExitIPPrecedesSourceIP 多网卡场景：出口 IP 优先于来源 IP。
// 即使来源 IP 恰好命中另一条存量主机，也应绑定到出口 IP 对应的主机。
func TestAgentRegisterExitIPPrecedesSourceIP(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Config = config.Default()
	sc.Config.Agent.RequireEnrollToken = false

	sfx := time.Now().UnixNano()
	srcIP := fmt.Sprintf("198.20.%d.%d", (sfx/250)%250, sfx%250)
	exitIP := fmt.Sprintf("198.21.%d.%d", (sfx/250)%250, (sfx+11)%250)
	decoyIP := fmt.Sprintf("198.22.%d.%d", (sfx/250)%250, (sfx+13)%250)

	var decoyID, targetID int64
	if err := db.QueryRow(`INSERT INTO hosts (ip, hostname, tenant_id, source)
		VALUES ($1,'decoy-source',1,'manual') RETURNING id`, srcIP).Scan(&decoyID); err != nil {
		t.Fatalf("插入来源主机失败: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO hosts (ip, hostname, tenant_id, source)
		VALUES ($1,'multi-nic',1,'consul_mgr') RETURNING id`, decoyIP).Scan(&targetID); err != nil {
		t.Fatalf("插入出口主机失败: %v", err)
	}
	if _, err := db.Exec(`UPDATE hosts SET exit_ip=$1 WHERE id=$2`, exitIP, targetID); err != nil {
		t.Fatalf("设置出口 IP 失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agents WHERE host_id IN ($1,$2)`, decoyID, targetID)
		db.Exec(`DELETE FROM hosts WHERE id IN ($1,$2)`, decoyID, targetID)
	})

	body := `{"hostname":"multi-nic","arch":"amd64","exit_ip":"` + exitIP + `"}`
	req := httptest.NewRequest("POST", "/api/agent/register", strings.NewReader(body))
	req.RemoteAddr = srcIP + ":12345"
	rec := httptest.NewRecorder()
	AgentRegister(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("注册失败 %d: %s", rec.Code, rec.Body.String())
	}

	var decoyAgent, targetAgent string
	db.QueryRow(`SELECT COALESCE(agent_id,'') FROM hosts WHERE id=$1`, decoyID).Scan(&decoyAgent)
	db.QueryRow(`SELECT COALESCE(agent_id,'') FROM hosts WHERE id=$1`, targetID).Scan(&targetAgent)
	if decoyAgent != "" {
		t.Fatal("来源 IP 命中的诱饵主机不应被绑定")
	}
	if targetAgent == "" {
		t.Fatal("应绑定到出口 IP 对应的主机")
	}
}

// TestAgentRegisterTunnelIPPrecedesExitIP 手工填写的 tunnel_ip 优先级最高：
// 即使出口 IP 命中另一台主机，也应绑定到隧道 IP 对应的主机。
func TestAgentRegisterTunnelIPPrecedesExitIP(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Config = config.Default()
	sc.Config.Agent.RequireEnrollToken = false

	sfx := time.Now().UnixNano()
	srcIP := fmt.Sprintf("198.23.%d.%d", (sfx/250)%250, sfx%250)
	tunnelIP := fmt.Sprintf("198.24.%d.%d", (sfx/250)%250, (sfx+3)%250)
	otherIP := fmt.Sprintf("198.25.%d.%d", (sfx/250)%250, (sfx+5)%250)

	var tunnelHostID, exitHostID int64
	if err := db.QueryRow(`INSERT INTO hosts (ip, hostname, tunnel_ip, tenant_id, source)
		VALUES ($1,'tunnel-host',$2,1,'manual') RETURNING id`, otherIP, tunnelIP).Scan(&tunnelHostID); err != nil {
		t.Fatalf("插入隧道主机失败: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO hosts (ip, hostname, exit_ip, tenant_id, source)
		VALUES ($1,'exit-host',$2,1,'consul_mgr') RETURNING id`, fmt.Sprintf("198.26.%d.%d", (sfx/250)%250, (sfx+7)%250), tunnelIP).Scan(&exitHostID); err != nil {
		t.Fatalf("插入出口主机失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agents WHERE host_id IN ($1,$2)`, tunnelHostID, exitHostID)
		db.Exec(`DELETE FROM hosts WHERE id IN ($1,$2)`, tunnelHostID, exitHostID)
	})

	body := `{"hostname":"tunnel-host","arch":"amd64","exit_ip":"` + tunnelIP + `"}`
	req := httptest.NewRequest("POST", "/api/agent/register", strings.NewReader(body))
	req.RemoteAddr = srcIP + ":12345"
	rec := httptest.NewRecorder()
	AgentRegister(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("注册失败 %d: %s", rec.Code, rec.Body.String())
	}

	var tunnelAgent, exitAgent string
	db.QueryRow(`SELECT COALESCE(agent_id,'') FROM hosts WHERE id=$1`, tunnelHostID).Scan(&tunnelAgent)
	db.QueryRow(`SELECT COALESCE(agent_id,'') FROM hosts WHERE id=$1`, exitHostID).Scan(&exitAgent)
	if tunnelAgent == "" {
		t.Fatal("应优先绑定到手工填写的隧道 IP 主机")
	}
	if exitAgent != "" {
		t.Fatal("出口 IP 命中的主机不应被绑定")
	}
}

// TestAgentRegisterAutoMergesTunnelDuplicates 注册命中多条同隧道 IP 的重复主机时，
// 应自动合并为一条并继续注册，而不是返回 409。
func TestAgentRegisterAutoMergesTunnelDuplicates(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Config = config.Default()
	sc.Config.Agent.RequireEnrollToken = false

	sfx := time.Now().UnixNano()
	srcIP := fmt.Sprintf("198.35.%d.%d", (sfx/250)%250, sfx%250)
	tunnel := fmt.Sprintf("198.36.%d.%d", (sfx/250)%250, (sfx+3)%250)
	ip1 := fmt.Sprintf("198.37.%d.%d", (sfx/250)%250, (sfx+5)%250)
	ip2 := fmt.Sprintf("198.38.%d.%d", (sfx/250)%250, (sfx+7)%250)

	var id1, id2 int64
	if err := db.QueryRow(`INSERT INTO hosts (ip, hostname, tunnel_ip, app_code, tenant_id, source)
		VALUES ($1,'dup-tunnel','`+tunnel+`','APP1',1,'consul_mgr') RETURNING id`, ip1).Scan(&id1); err != nil {
		t.Fatalf("插入重复主机1失败: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO hosts (ip, hostname, tunnel_ip, biz_group, tenant_id, source)
		VALUES ($1,'dup-tunnel',$2,'G1',1,'manual') RETURNING id`, ip2, tunnel).Scan(&id2); err != nil {
		t.Fatalf("插入重复主机2失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agents WHERE host_id IN ($1,$2)`, id1, id2)
		db.Exec(`DELETE FROM hosts WHERE id IN ($1,$2)`, id1, id2)
	})

	body := `{"hostname":"dup-tunnel","arch":"amd64","exit_ip":"` + tunnel + `"}`
	req := httptest.NewRequest("POST", "/api/agent/register", strings.NewReader(body))
	req.RemoteAddr = srcIP + ":12345"
	rec := httptest.NewRecorder()
	AgentRegister(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("注册失败 %d: %s", rec.Code, rec.Body.String())
	}

	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE tunnel_ip=$1`, tunnel).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("重复主机应自动合并为 1 条，实际 %d", total)
	}
	var targetID int64
	var agentID, appCode, bizGroup string
	if err := db.QueryRow(`SELECT id, COALESCE(agent_id,''), app_code, biz_group
		FROM hosts WHERE tunnel_ip=$1`, tunnel).Scan(&targetID, &agentID, &appCode, &bizGroup); err != nil {
		t.Fatal(err)
	}
	if agentID == "" {
		t.Fatal("合并后的主机应绑定 Agent")
	}
	if appCode != "APP1" || bizGroup != "G1" {
		t.Fatalf("合并后业务字段应保留: app=%s group=%s", appCode, bizGroup)
	}
}
