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

func TestBuildInstallCommandRemovesStaleBinary(t *testing.T) {
	cmd := buildInstallCommand("http://10.1.230.50:8090", "amd64", "tok-123", false)

	if !strings.HasPrefix(cmd, "rm -f /tmp/upgrade-agent") {
		t.Fatalf("安装命令必须先删除旧二进制，got:\n%s", cmd)
	}
	for _, want := range []string{
		"wget -c",
		"'http://10.1.230.50:8090/api/agent/binary/linux-amd64'",
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
	cmd := buildInstallCommand("http://10.1.230.50:8090", "amd64", "tok-123", true)
	if !strings.HasSuffix(cmd, "--force") {
		t.Fatalf("强制重新注册命令应以 --force 结尾，got:\n%s", cmd)
	}
	if !strings.Contains(cmd, "--enroll-token 'tok-123' --force") {
		t.Fatalf("--force 必须与 --enroll-token 同时出现，got:\n%s", cmd)
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
