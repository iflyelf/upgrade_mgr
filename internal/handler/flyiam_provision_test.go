package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/rbac"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// fakeFlyIAM 返回最小用户导出数据，供 provision 单测使用。
func fakeFlyIAM(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/users/export" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": []map[string]any{
				{"username": "fly-a", "displayName": "甲", "email": "a@example.com",
					"properties": map[string]string{"empCode": "E1001"}},
				{"username": "fly-b", "displayName": "乙"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFlyIAMPromisionLocalUser 一键创建本地账号：随机密码、字段同步、重复拒绝、未知用户。
func TestFlyIAMPromisionLocalUser(t *testing.T) {
	db, _ := setupHostDB(t)
	srv := fakeFlyIAM(t)
	sc := &svc.ServiceContext{
		DB:     db,
		Config: &config.Config{FlyIAM: config.FlyIAMConfig{Endpoint: srv.URL, ServiceToken: "tok"}},
		RBAC:   rbac.NewService(db),
	}

	// 不存在的用户 → 404
	req := httptest.NewRequest("POST", "/api/flyiam/users/nobody-x/provision", strings.NewReader(`{}`))
	req.SetPathValue("username", "nobody-x")
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	FlyIAMProvisionLocalUser(sc)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知用户应 404，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// fly-a 第一次创建 → 200，生成密码
	t.Cleanup(func() {
		db.Exec(`DELETE FROM user_field_values WHERE user_id LIKE 'fly-a%'`)
		db.Exec(`DELETE FROM local_users WHERE username LIKE 'fly-a%'`)
	})
	req = httptest.NewRequest("POST", "/api/flyiam/users/fly-a/provision",
		strings.NewReader(`{"sync_fields":true}`))
	req.SetPathValue("username", "fly-a")
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec = httptest.NewRecorder()
	FlyIAMProvisionLocalUser(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Username  string `json:"username"`
			Password  string `json:"password"`
			Generated bool   `json:"generated"`
			Synced    int    `json:"synced_fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Username != "fly-a" || !out.Data.Generated || len(out.Data.Password) < 12 {
		t.Fatalf("响应不符: %+v", out.Data)
	}
	var display string
	if err := db.QueryRow(`SELECT display_name FROM local_users WHERE username='fly-a'`).Scan(&display); err != nil {
		t.Fatal(err)
	}
	if display != "甲" {
		t.Fatalf("display 应来自 FlyIAM, got %q", display)
	}
	var emp string
	if err := db.QueryRow(`SELECT value FROM user_field_values WHERE user_id='fly-a' AND field_key='empCode'`).Scan(&emp); err != nil {
		t.Fatalf("扩展字段未同步: %v", err)
	}
	if emp != "E1001" {
		t.Fatalf("empCode=%q", emp)
	}
	if out.Data.Synced != 1 {
		t.Fatalf("synced=%d", out.Data.Synced)
	}

	// 重复创建 → 409
	req = httptest.NewRequest("POST", "/api/flyiam/users/fly-a/provision", strings.NewReader(`{}`))
	req.SetPathValue("username", "fly-a")
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec = httptest.NewRecorder()
	FlyIAMProvisionLocalUser(sc)(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复创建应 409，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestFlyIAMPromisionCustomPassword 指定密码时不返回密码，generated=false。
func TestFlyIAMPromisionCustomPassword(t *testing.T) {
	db, _ := setupHostDB(t)
	srv := fakeFlyIAM(t)
	sc := &svc.ServiceContext{
		DB:     db,
		Config: &config.Config{FlyIAM: config.FlyIAMConfig{Endpoint: srv.URL, ServiceToken: "tok"}},
		RBAC:   rbac.NewService(db),
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM local_users WHERE username='fly-b'`) })

	// 过短密码 → 400
	shortReq := httptest.NewRequest("POST", "/api/flyiam/users/fly-b/provision",
		strings.NewReader(`{"password":"short"}`))
	shortReq.SetPathValue("username", "fly-b")
	shortReq = shortReq.WithContext(auth.WithUser(shortReq.Context(), hostTestUser(1)))
	shortRec := httptest.NewRecorder()
	FlyIAMProvisionLocalUser(sc)(shortRec, shortReq)
	if shortRec.Code != http.StatusBadRequest {
		t.Fatalf("过短密码应 400，实际 %d: %s", shortRec.Code, shortRec.Body.String())
	}

	req := httptest.NewRequest("POST", "/api/flyiam/users/fly-b/provision",
		strings.NewReader(`{"password":"Secret123!"}`))
	req.SetPathValue("username", "fly-b")
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	FlyIAMProvisionLocalUser(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data["generated"] == true {
		t.Fatal("指定密码时不应标记 generated")
	}
	if _, ok := out.Data["password"]; ok {
		t.Fatal("指定密码时不应回显密码")
	}
}

// TestGeneratePassword 随机密码长度与字符集。
func TestGeneratePassword(t *testing.T) {
	pwd, err := generatePassword(16)
	if err != nil {
		t.Fatal(err)
	}
	if len(pwd) != 16 {
		t.Fatalf("长度应为 16, got %d", len(pwd))
	}
	for _, c := range pwd {
		if !strings.ContainsRune(provisionPasswordCharset, c) {
			t.Fatalf("非法字符 %q", c)
		}
	}
	// 两次生成不应相同
	other, err := generatePassword(16)
	if err != nil {
		t.Fatal(err)
	}
	if pwd == other {
		t.Fatal("两次生成不应相同")
	}
}
