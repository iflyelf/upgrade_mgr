package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
)

// insertTestEnrollToken 插入测试令牌；expiresAt=nil 表示永久。
func insertTestEnrollToken(t *testing.T, db *sql.DB, token, tokenType string,
	expiresAt any, maxUses, usedCount int, enabled bool, cidrs []string, tenantID int64, createdBy string) int64 {
	t.Helper()
	if cidrs == nil {
		cidrs = []string{}
	}
	var id int64
	if err := db.QueryRow(`
		INSERT INTO enroll_tokens
			(token_hash, token_type, expires_at, max_uses, used_count, enabled,
			 allowed_cidrs, tenant_id, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id`,
		agentgw.HashToken(token), tokenType, expiresAt, maxUses, usedCount, enabled,
		pq.Array(cidrs), tenantID, createdBy).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func consumeTestToken(t *testing.T, db *sql.DB, token, ip string) (int64, bool) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	tenantID, ok := consumeEnrollTokenTx(context.Background(), tx, token, ip)
	if !ok {
		return 0, false
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return tenantID, true
}

// TestConsumeEnrollTokenTypes 一次性令牌保持只能用一次；自动化令牌支持永久、
// 次数上限、CIDR、注销/过期。
func TestConsumeEnrollTokenTypes(t *testing.T) {
	db, _ := setupHostDB(t)
	sfx := time.Now().UnixNano()
	creator := fmt.Sprintf("test-consume-%d", sfx)
	t.Cleanup(func() { db.Exec(`DELETE FROM enroll_tokens WHERE created_by=$1`, creator) })

	oneTime := fmt.Sprintf("one-time-%d", sfx)
	oneTimeID := insertTestEnrollToken(t, db, oneTime, "one_time",
		time.Now().Add(10*time.Minute), 1, 0, true, nil, 1, creator)
	tenantID, ok := consumeTestToken(t, db, oneTime, "10.1.2.3")
	if !ok || tenantID != 1 {
		t.Fatalf("一次性令牌首次消费失败: tenant=%d ok=%v", tenantID, ok)
	}
	if _, ok := consumeTestToken(t, db, oneTime, "10.1.2.3"); ok {
		t.Fatal("一次性令牌不应被消费第二次")
	}
	var used sql.NullTime
	if err := db.QueryRow(`SELECT used_at FROM enroll_tokens WHERE id=$1`, oneTimeID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if !used.Valid {
		t.Fatal("一次性令牌消费后 used_at 应非空")
	}

	automation := fmt.Sprintf("automation-max-%d", sfx)
	automationID := insertTestEnrollToken(t, db, automation, "automation",
		nil, 2, 0, true, nil, 1, creator)
	for i := 0; i < 2; i++ {
		if _, ok := consumeTestToken(t, db, automation, "10.1.2.3"); !ok {
			t.Fatalf("自动化令牌第 %d 次消费应成功", i+1)
		}
	}
	if _, ok := consumeTestToken(t, db, automation, "10.1.2.3"); ok {
		t.Fatal("自动化令牌超过 max_uses 后不应成功")
	}
	var usedCount int
	if err := db.QueryRow(`SELECT used_count FROM enroll_tokens WHERE id=$1`, automationID).Scan(&usedCount); err != nil {
		t.Fatal(err)
	}
	if usedCount != 2 {
		t.Fatalf("used_count = %d, want 2", usedCount)
	}

	cidrToken := fmt.Sprintf("automation-cidr-%d", sfx)
	insertTestEnrollToken(t, db, cidrToken, "automation", nil, 0, 0, true,
		[]string{"10.0.0.0/8"}, 1, creator)
	if _, ok := consumeTestToken(t, db, cidrToken, "10.9.8.7"); !ok {
		t.Fatal("CIDR 内 IP 应能消费")
	}
	if _, ok := consumeTestToken(t, db, cidrToken, "192.0.2.1"); ok {
		t.Fatal("CIDR 外 IP 不应能消费")
	}

	revoked := fmt.Sprintf("automation-revoked-%d", sfx)
	insertTestEnrollToken(t, db, revoked, "automation", nil, 0, 0, false, nil, 1, creator)
	if _, ok := consumeTestToken(t, db, revoked, "10.1.2.3"); ok {
		t.Fatal("已注销令牌不应能消费")
	}

	expired := fmt.Sprintf("automation-expired-%d", sfx)
	insertTestEnrollToken(t, db, expired, "automation",
		time.Now().Add(-time.Minute), 0, 0, true, nil, 1, creator)
	if _, ok := consumeTestToken(t, db, expired, "10.1.2.3"); ok {
		t.Fatal("已过期令牌不应能消费")
	}
}

// TestAutomationEnrollTokenAPI 覆盖页面使用的创建、列表、注销、轮换与安装命令。
func TestAutomationEnrollTokenAPI(t *testing.T) {
	db, sc := setupHostDB(t)
	sfx := time.Now().UnixNano()
	name := fmt.Sprintf("api-auto-%d", sfx)
	t.Cleanup(func() { db.Exec(`DELETE FROM enroll_tokens WHERE name=$1`, name) })

	user := hostTestUser(1)
	call := func(method, path, body string, handler http.HandlerFunc, setPath bool) *httptest.ResponseRecorder {
		t.Helper()
		var reader *strings.Reader
		if body == "" {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		if setPath {
			parts := strings.Split(strings.Trim(path, "/"), "/")
			req.SetPathValue("id", parts[len(parts)-2])
		}
		req = req.WithContext(auth.WithUser(req.Context(), user))
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}

	createBody := fmt.Sprintf(`{"name":%q,"permanent":true,"max_uses":0,
		"allowed_cidrs":["10.0.0.0/8"],"os":"linux","arch":"amd64"}`, name)
	rec := call(http.MethodPost, "/api/agent/enroll-tokens", createBody, AutomationEnrollTokenCreate(sc), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建自动化令牌失败 %d: %s", rec.Code, rec.Body.String())
	}
	var createOut struct {
		Data automationTokenCreatedResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &createOut); err != nil {
		t.Fatal(err)
	}
	created := createOut.Data
	if created.ID <= 0 || !strings.HasPrefix(created.Token, automationTokenPrefix) {
		t.Fatalf("创建响应异常: %+v", created)
	}
	if created.ExpiresAt != nil {
		t.Fatalf("永久令牌 expires_at 应为 nil: %+v", created.ExpiresAt)
	}
	if !strings.Contains(created.InstallCommand, created.Token) {
		t.Fatalf("安装命令未包含令牌: %s", created.InstallCommand)
	}

	// 列表必须能查到，且不回显完整 token。
	rec = call(http.MethodGet, "/api/agent/enroll-tokens", "", AutomationEnrollTokenList(sc), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表失败 %d: %s", rec.Code, rec.Body.String())
	}
	var listOut struct {
		Data struct {
			List []struct {
				ID           int64    `json:"id"`
				Name         string   `json:"name"`
				TokenPrefix  string   `json:"token_prefix"`
				ExpiresAt    *string  `json:"expires_at"`
				Status       string   `json:"status"`
				AllowedCIDRs []string `json:"allowed_cidrs"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listOut); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listOut.Data.List {
		if item.ID != created.ID {
			continue
		}
		found = true
		if item.Name != name || item.TokenPrefix == "" || item.ExpiresAt != nil ||
			item.Status != "active" || len(item.AllowedCIDRs) != 1 {
			t.Fatalf("列表项不符: %+v", item)
		}
	}
	if !found {
		t.Fatalf("列表未找到新建令牌 %d", created.ID)
	}

	// 轮换：旧 token 立即失效，新 token 可消费。
	rec = call(http.MethodPost, "/api/agent/enroll-tokens/"+strconv.FormatInt(created.ID, 10)+"/rotate",
		"", AutomationEnrollTokenRotate(sc), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("轮换失败 %d: %s", rec.Code, rec.Body.String())
	}
	var rotateOut struct {
		Data automationTokenCreatedResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rotateOut); err != nil {
		t.Fatal(err)
	}
	rotated := rotateOut.Data
	if rotated.ID == created.ID || rotated.Token == created.Token {
		t.Fatalf("轮换后应生成新令牌: old=%+v new=%+v", created, rotated)
	}
	if _, ok := consumeTestToken(t, db, created.Token, "10.1.2.3"); ok {
		t.Fatal("轮换后的旧 token 不应能消费")
	}
	if _, ok := consumeTestToken(t, db, rotated.Token, "10.1.2.3"); !ok {
		t.Fatal("轮换后的新 token 应能消费")
	}

	// 注销后新 token 也不能再使用。
	rec = call(http.MethodPost, "/api/agent/enroll-tokens/"+strconv.FormatInt(rotated.ID, 10)+"/revoke",
		"", AutomationEnrollTokenRevoke(sc), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("注销失败 %d: %s", rec.Code, rec.Body.String())
	}
	var enabled bool
	var revokedAt sql.NullTime
	if err := db.QueryRow(`SELECT enabled, revoked_at FROM enroll_tokens WHERE id=$1`, rotated.ID).
		Scan(&enabled, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if enabled || !revokedAt.Valid {
		t.Fatalf("注销后状态异常: enabled=%v revoked_at=%+v", enabled, revokedAt)
	}
	if _, ok := consumeTestToken(t, db, rotated.Token, "10.1.2.3"); ok {
		t.Fatal("注销后的 token 不应能消费")
	}
}

// TestResolveAutomationExpiry 覆盖永久、固定时长、自定义时间和非法参数。
func TestResolveAutomationExpiry(t *testing.T) {
	if got, err := resolveAutomationExpiry(automationTokenCreateRequest{Permanent: true}); err != nil || got != nil {
		t.Fatalf("永久令牌应为 nil: got=%v err=%v", got, err)
	}
	now := time.Now()
	got, err := resolveAutomationExpiry(automationTokenCreateRequest{ExpiresIn: "1h"})
	if err != nil || got == nil || got.Before(now.Add(59*time.Minute)) || got.After(now.Add(61*time.Minute)) {
		t.Fatalf("1h 有效期解析错误: got=%v err=%v", got, err)
	}
	future := now.Add(24 * time.Hour).Format(time.RFC3339)
	got, err = resolveAutomationExpiry(automationTokenCreateRequest{ExpiresAt: future})
	if err != nil || got == nil {
		t.Fatalf("自定义到期时间解析失败: got=%v err=%v", got, err)
	}
	past := now.Add(-time.Hour).Format(time.RFC3339)
	if _, err := resolveAutomationExpiry(automationTokenCreateRequest{ExpiresAt: past}); err == nil {
		t.Fatal("过去的到期时间应报错")
	}
	if _, err := resolveAutomationExpiry(automationTokenCreateRequest{}); err == nil {
		t.Fatal("未指定有效期应报错")
	}
	if _, err := resolveAutomationExpiry(automationTokenCreateRequest{Permanent: true, ExpiresIn: "1h"}); err == nil {
		t.Fatal("永久与 expires_in 冲突应报错")
	}
}

// TestNormalizeAutomationCIDRs CIDR 去重、规范化与非法值。
func TestNormalizeAutomationCIDRs(t *testing.T) {
	got, err := normalizeAutomationCIDRs([]string{" 10.1.2.3/8 ", "10.0.0.0/8", ""})
	if err != nil || len(got) != 1 || got[0] != "10.0.0.0/8" {
		t.Fatalf("CIDR 规范化错误: %v %v", got, err)
	}
	if _, err := normalizeAutomationCIDRs([]string{"not-a-cidr"}); err == nil {
		t.Fatal("非法 CIDR 应报错")
	}
}

// TestAutomationEnrollTokenTenantIsolation 普通管理员不能创建/查看其他租户的
// 自动化令牌；superadmin 可以跨租户查看。
func TestAutomationEnrollTokenTenantIsolation(t *testing.T) {
	db, sc := setupHostDB(t)
	sfx := time.Now().UnixNano()
	for _, id := range []int{901, 902} {
		if _, err := db.Exec(`INSERT INTO tenants (id,name) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`,
			id, fmt.Sprintf("auto_token_tenant_%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM tenants WHERE id IN (901,902)`) })

	name := fmt.Sprintf("tenant-isolation-%d", sfx)
	token := fmt.Sprintf("tenant-isolation-token-%d", sfx)
	id := insertTestEnrollToken(t, db, token, "automation", nil, 0, 0, true, nil, 902, "test-tenant")
	if _, err := db.Exec(`UPDATE enroll_tokens SET name=$2 WHERE id=$1`, id, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM enroll_tokens WHERE id=$1`, id) })

	parseList := func(rec *httptest.ResponseRecorder) []int64 {
		t.Helper()
		var out struct {
			Data struct {
				List []struct {
					ID int64 `json:"id"`
				} `json:"list"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		ids := make([]int64, 0, len(out.Data.List))
		for _, item := range out.Data.List {
			ids = append(ids, item.ID)
		}
		return ids
	}

	admin := hostTestUser(901)
	req := httptest.NewRequest("GET", "/api/agent/enroll-tokens?tenant_id=902", nil)
	req = req.WithContext(auth.WithUser(req.Context(), admin))
	rec := httptest.NewRecorder()
	AutomationEnrollTokenList(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表失败 %d: %s", rec.Code, rec.Body.String())
	}
	for _, got := range parseList(rec) {
		if got == id {
			t.Fatal("普通管理员不应看到其他租户的自动化令牌")
		}
	}

	body := fmt.Sprintf(`{"name":"forbidden-%d","tenant_id":902,"permanent":true}`, sfx)
	req = httptest.NewRequest("POST", "/api/agent/enroll-tokens", strings.NewReader(body))
	req = req.WithContext(auth.WithUser(req.Context(), admin))
	rec = httptest.NewRecorder()
	AutomationEnrollTokenCreate(sc)(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通管理员跨租户创建应返回 403，实际 %d: %s", rec.Code, rec.Body.String())
	}

	super := &auth.User{ID: "super", Username: "super", IsAdmin: true, TenantID: 1,
		Roles: []string{auth.RoleSuperAdmin}}
	req = httptest.NewRequest("GET", "/api/agent/enroll-tokens", nil)
	req = req.WithContext(auth.WithUser(req.Context(), super))
	rec = httptest.NewRecorder()
	AutomationEnrollTokenList(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("superadmin 列表失败 %d: %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, got := range parseList(rec) {
		if got == id {
			found = true
		}
	}
	if !found {
		t.Fatal("superadmin 应能看到其他租户的自动化令牌")
	}
}

// TestAgentRegisterWithAutomationToken 自动化令牌可被多台主机复用，
// 且主机归属令牌租户；达到 max_uses 后拒绝新主机。
func TestAgentRegisterWithAutomationToken(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Config = config.Default()
	sc.Config.Agent.RequireEnrollToken = true

	sfx := time.Now().UnixNano()
	if _, err := db.Exec(`INSERT INTO tenants (id,name) VALUES (902,$1) ON CONFLICT (id) DO NOTHING`,
		fmt.Sprintf("auto_reg_%d", sfx)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM tenants WHERE id=902`) })

	token := fmt.Sprintf("auto-register-token-%d", sfx)
	creator := fmt.Sprintf("test-auto-register-%d", sfx)
	tokenID := insertTestEnrollToken(t, db, token, "automation", nil, 2, 0, true, nil, 902, creator)
	ips := []string{
		fmt.Sprintf("198.51.100.%d", (sfx/3)%200+1),
		fmt.Sprintf("198.51.100.%d", (sfx/3)%200+11),
		fmt.Sprintf("198.51.100.%d", (sfx/3)%200+21),
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agents WHERE ip = ANY($1)`, pq.Array(ips))
		db.Exec(`DELETE FROM hosts WHERE ip = ANY($1)`, pq.Array(ips))
		db.Exec(`DELETE FROM enroll_tokens WHERE id=$1`, tokenID)
	})

	post := func(ip string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"hostname":"auto-reg-%d","arch":"amd64","enroll_token":%q}`, sfx, token)
		req := httptest.NewRequest("POST", "/api/agent/register", strings.NewReader(body))
		req.RemoteAddr = ip + ":12345"
		rec := httptest.NewRecorder()
		AgentRegister(sc)(rec, req)
		return rec
	}

	for i := 0; i < 2; i++ {
		if rec := post(ips[i]); rec.Code != http.StatusOK {
			t.Fatalf("第 %d 台主机注册失败 %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	var used int
	if err := db.QueryRow(`SELECT used_count FROM enroll_tokens WHERE id=$1`, tokenID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 2 {
		t.Fatalf("used_count = %d, want 2", used)
	}
	var tenantCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE ip=$1 AND tenant_id=902`, ips[0]).Scan(&tenantCount); err != nil {
		t.Fatal(err)
	}
	if tenantCount != 1 {
		t.Fatalf("自动化令牌注册的主机应归属租户 902，实际 count=%d", tenantCount)
	}

	if rec := post(ips[2]); rec.Code != http.StatusUnauthorized {
		t.Fatalf("超过 max_uses 的注册应返回 401，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if err := db.QueryRow(`SELECT used_count FROM enroll_tokens WHERE id=$1`, tokenID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 2 {
		t.Fatalf("失败注册不应增加 used_count，实际 %d", used)
	}
}

// TestAutomationEnrollTokenCustomExpiry 页面自定义时长/时间应落到 expires_at。
func TestAutomationEnrollTokenCustomExpiry(t *testing.T) {
	db, sc := setupHostDB(t)
	sfx := time.Now().UnixNano()
	name := fmt.Sprintf("api-expiry-%d", sfx)
	t.Cleanup(func() { db.Exec(`DELETE FROM enroll_tokens WHERE name=$1`, name) })

	body := fmt.Sprintf(`{"name":%q,"expires_in":"1h","max_uses":10,"os":"linux","arch":"amd64"}`, name)
	req := httptest.NewRequest("POST", "/api/agent/enroll-tokens", strings.NewReader(body))
	req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
	rec := httptest.NewRecorder()
	AutomationEnrollTokenCreate(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建自定义时长令牌失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data automationTokenCreatedResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.ExpiresAt == nil {
		t.Fatal("自定义时长令牌 expires_at 不应为空")
	}
	now := time.Now()
	if out.Data.ExpiresAt.Before(now.Add(59*time.Minute)) || out.Data.ExpiresAt.After(now.Add(61*time.Minute)) {
		t.Fatalf("expires_at = %v, want ~1h", out.Data.ExpiresAt)
	}
}
