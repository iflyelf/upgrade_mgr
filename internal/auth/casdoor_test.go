package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockCasdoor 模拟 Casdoor 的 token 与用户接口。
func mockCasdoor(t *testing.T, user map[string]any) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析表单失败: %v", err)
		}
		if r.Form.Get("code") != "good-code" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at-123", "token_type": "Bearer"})
	})
	mux.HandleFunc("/api/get-account", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-123" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": user})
	})
	return httptest.NewServer(mux)
}

func newTestCasdoor(endpoint, org string) *Casdoor {
	return NewCasdoor(CasdoorOptions{
		Endpoint: endpoint, ClientID: "cid", ClientSecret: "csecret",
		Organization: org, Application: "app",
		RedirectURI: "http://localhost/api/auth/callback",
		SessionSecret: "test-secret-at-least-16-bytes-long",
	})
}

// TestCasdoorFullFlow 完整授权码流程：LoginURL → 回调 → 会话可被 Authenticate 识别。
func TestCasdoorFullFlow(t *testing.T) {
	srv := mockCasdoor(t, map[string]any{
		"name": "alice", "displayName": "Alice", "owner": "myorg", "isAdmin": true,
	})
	defer srv.Close()
	c := newTestCasdoor(srv.URL, "myorg")

	loginURL := c.LoginURL("")
	if !strings.Contains(loginURL, "client_id=cid") || !strings.Contains(loginURL, "state=") {
		t.Fatalf("LoginURL 缺少必要参数: %s", loginURL)
	}
	// 取出服务端签发的 state，模拟浏览器回传
	state := loginURL[strings.Index(loginURL, "state=")+len("state="):]
	if i := strings.IndexByte(state, '&'); i >= 0 {
		state = state[:i]
	}
	t.Logf("✅ LoginURL 已带签名 state")

	u, token, err := c.HandleCallback(context.Background(), "good-code", state)
	if err != nil {
		t.Fatalf("回调失败: %v", err)
	}
	if u.Username != "alice" || !u.IsAdmin || u.TenantID != 1 {
		t.Fatalf("用户信息映射错误: %+v", u)
	}
	t.Logf("✅ 授权码换取用户成功: %s admin=%v tenant=%d roles=%v", u.Username, u.IsAdmin, u.TenantID, u.Roles)

	// 会话应能被 Authenticate 识别（证明不再是恒 ErrUnauthorized 的占位）
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	got, err := c.Authenticate(req)
	if err != nil {
		t.Fatalf("会话认证失败: %v", err)
	}
	if got.Username != "alice" || !got.IsAdmin {
		t.Fatalf("会话解析错误: %+v", got)
	}
	t.Logf("✅ Authenticate 可识别 SSO 会话（占位已被真实实现替代）")
}

// TestCasdoorRejectsForgedState 伪造 state 必须被拒（防 CSRF）。
func TestCasdoorRejectsForgedState(t *testing.T) {
	srv := mockCasdoor(t, map[string]any{"name": "alice", "owner": "myorg"})
	defer srv.Close()
	c := newTestCasdoor(srv.URL, "myorg")

	if _, _, err := c.HandleCallback(context.Background(), "good-code", "forged-state"); err == nil {
		t.Fatal("伪造 state 必须被拒绝")
	}
	t.Log("✅ 伪造 state 被拒绝（CSRF 防护生效）")
}

// TestCasdoorRejectsWrongOrg 跨组织用户必须被拒。
func TestCasdoorRejectsWrongOrg(t *testing.T) {
	srv := mockCasdoor(t, map[string]any{"name": "bob", "owner": "otherorg"})
	defer srv.Close()
	c := newTestCasdoor(srv.URL, "myorg")

	state := c.signState("")
	if _, _, err := c.HandleCallback(context.Background(), "good-code", state); err == nil {
		t.Fatal("跨组织用户必须被拒绝")
	}
	t.Log("✅ 跨组织用户被拒绝")
}

// TestCasdoorRejectsForbiddenUser 被禁用用户必须被拒。
func TestCasdoorRejectsForbiddenUser(t *testing.T) {
	srv := mockCasdoor(t, map[string]any{"name": "carol", "owner": "myorg", "isForbidden": true})
	defer srv.Close()
	c := newTestCasdoor(srv.URL, "myorg")

	state := c.signState("")
	if _, _, err := c.HandleCallback(context.Background(), "good-code", state); err == nil {
		t.Fatal("被禁用用户必须被拒绝")
	}
	t.Log("✅ 被禁用用户被拒绝")
}

// TestCasdoorBadCode 无效授权码必须失败。
func TestCasdoorBadCode(t *testing.T) {
	srv := mockCasdoor(t, map[string]any{"name": "alice", "owner": "myorg"})
	defer srv.Close()
	c := newTestCasdoor(srv.URL, "myorg")

	state := c.signState("")
	if _, _, err := c.HandleCallback(context.Background(), "bad-code", state); err == nil {
		t.Fatal("无效授权码必须失败")
	}
	t.Log("✅ 无效授权码被拒绝")
}
