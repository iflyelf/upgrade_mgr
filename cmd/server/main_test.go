package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestServerIntegration 端到端验证：独立启动（无 Consul）、登录、鉴权、主机、设置。
// 需要 DATABASE_URL；未设置则跳过。
func TestServerIntegration(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过集成测试")
	}
	os.Setenv("AUTH_PROVIDER", "builtin")
	os.Setenv("ADMIN_USERNAME", "admin")
	os.Setenv("ADMIN_PASSWORD", "admin123")
	os.Setenv("SESSION_SECRET", "integration-test-secret-32bytes!!")

	c, err := config.Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := c.ValidateRequired(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	sc := svc.New(c)

	mux := http.NewServeMux()
	registerRoutes(mux, sc)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ts.Client()
	jar := &cookieJar{}
	client.Jar = jar

	// health
	if body, code := get(t, client, ts.URL+"/health"); code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("health failed: %d %s", code, body)
	}

	// 未认证访问 hosts → 401
	if _, code := get(t, client, ts.URL+"/api/hosts"); code != 401 {
		t.Fatalf("expected 401 unauth, got %d", code)
	}

	// 错误密码 → 401
	if _, code := postJSON(t, client, ts.URL+"/api/auth/login", `{"username":"admin","password":"wrong"}`); code != 401 {
		t.Fatalf("expected 401 bad login, got %d", code)
	}

	// 正确登录
	if body, code := postJSON(t, client, ts.URL+"/api/auth/login", `{"username":"admin","password":"admin123"}`); code != 200 {
		t.Fatalf("login failed: %d %s", code, body)
	}

	// userinfo
	if body, code := get(t, client, ts.URL+"/api/auth/userinfo"); code != 200 || !strings.Contains(body, "admin") {
		t.Fatalf("userinfo failed: %d %s", code, body)
	}

	// hosts（可能为空，但应 200 且结构正确）
	body, code := get(t, client, ts.URL+"/api/hosts?size=5")
	if code != 200 {
		t.Fatalf("hosts failed: %d %s", code, body)
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Total int `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("hosts json: %v (%s)", err, body)
	}

	// settings（管理员）
	if body, code := get(t, client, ts.URL+"/api/settings"); code != 200 || !strings.Contains(body, "consul.address") {
		t.Fatalf("settings failed: %d %s", code, body)
	}

	// Consul 未启用 → sync 返回 503
	if _, code := postJSON(t, client, ts.URL+"/api/hosts/sync", ""); code != 503 {
		t.Fatalf("expected 503 sync w/o consul, got %d", code)
	}

	// 前端 index
	if body, code := get(t, client, ts.URL+"/"); code != 200 || !strings.Contains(body, "升级控制平台") {
		t.Fatalf("index failed: %d", code)
	}
}

func get(t *testing.T, c *http.Client, url string) (string, int) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode
}

func postJSON(t *testing.T, c *http.Client, url, body string) (string, int) {
	t.Helper()
	resp, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode
}

// cookieJar 最小 cookie 容器（保留会话）。
type cookieJar struct{ cookies []*http.Cookie }

func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) { j.cookies = append(j.cookies, cs...) }
func (j *cookieJar) Cookies(_ *url.URL) []*http.Cookie        { return j.cookies }
