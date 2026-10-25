package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/agentgw"
	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/rbac"
)

// TestTenantIsolation_Components 验证租户隔离真实生效：
// A 租户用户看不到、改不了、删不掉 B 租户的组件；superadmin 可跨租户。
func TestTenantIsolation_Components(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	// 准备两个租户
	for _, id := range []int{901, 902} {
		db.Exec(`INSERT INTO tenants (id, name) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`,
			id, "iso_t"+string(rune('0'+id%10)))
	}
	defer db.Exec(`DELETE FROM tenants WHERE id IN (901,902)`)

	repo := component.NewRepo(db)
	scA := scope.New(901, false)
	scB := scope.New(902, false)
	scSuper := scope.New(901, true)

	db.Exec(`DELETE FROM components WHERE name IN ('iso_a','iso_b')`)
	defer db.Exec(`DELETE FROM components WHERE name IN ('iso_a','iso_b')`)

	idA, err := repo.Create(ctx, scA, &component.Component{Name: "iso_a", DeployMode: "replace"})
	if err != nil {
		t.Fatalf("A 建组件失败: %v", err)
	}
	idB, err := repo.Create(ctx, scB, &component.Component{Name: "iso_b", DeployMode: "replace"})
	if err != nil {
		t.Fatalf("B 建组件失败: %v", err)
	}

	// 1) 列表隔离
	listA, _ := repo.List(ctx, scA)
	for _, c := range listA {
		if c.Name == "iso_b" {
			t.Fatal("❌ 租户 A 看到了租户 B 的组件（隔离失效）")
		}
	}
	t.Log("✅ 列表隔离生效：A 看不到 B 的组件")

	// 2) 跨租户读取必须失败
	if _, err := repo.Get(ctx, scA, idB); err == nil {
		t.Fatal("❌ 租户 A 成功读取了租户 B 的组件（越权）")
	}
	t.Log("✅ 跨租户读取被拒绝")

	// 3) 跨租户修改必须失败
	err = repo.Update(ctx, scA, &component.Component{ID: idB, DisplayName: "hacked", DeployMode: "replace"})
	if err == nil {
		t.Fatal("❌ 租户 A 成功修改了租户 B 的组件（越权）")
	}
	var name string
	db.QueryRow(`SELECT COALESCE(display_name,'') FROM components WHERE id=$1`, idB).Scan(&name)
	if name == "hacked" {
		t.Fatal("❌ 租户 B 的数据被篡改")
	}
	t.Log("✅ 跨租户修改被拒绝，数据未被篡改")

	// 4) 跨租户删除必须失败
	if err := repo.Delete(ctx, scA, idB); err == nil {
		t.Fatal("❌ 租户 A 成功删除了租户 B 的组件（越权）")
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(1) FROM components WHERE id=$1`, idB).Scan(&cnt)
	if cnt != 1 {
		t.Fatal("❌ 租户 B 的组件被删除")
	}
	t.Log("✅ 跨租户删除被拒绝，数据仍在")

	// 5) superadmin 可跨租户
	if _, err := repo.Get(ctx, scSuper, idB); err != nil {
		t.Fatalf("❌ superadmin 应可跨租户读取，实际失败: %v", err)
	}
	t.Log("✅ superadmin 跨租户读取正常")

	// 6) 本租户操作正常
	if _, err := repo.Get(ctx, scA, idA); err != nil {
		t.Fatalf("❌ 本租户读取应成功: %v", err)
	}
	t.Log("✅ 本租户操作正常")
}

// TestReqScope_UnauthenticatedFailsClosed 未认证请求必须 fail-close 到空集。
func TestReqScope_UnauthenticatedFailsClosed(t *testing.T) {
	s := scope.ForUser(nil)
	if s.CrossTenant {
		t.Fatal("❌ 未认证用户不能跨租户")
	}
	cond, args := s.Where("", 1)
	if cond == "TRUE" {
		t.Fatal("❌ 未认证用户不能返回恒真条件")
	}
	if len(args) != 1 || args[0].(int64) != -1 {
		t.Fatalf("❌ 应限定到空集 tenant_id=-1，实际 %v", args)
	}
	t.Log("✅ 未认证 fail-close 到空集")
}

// TestScopeFromRealUser 验证从真实 auth.User 推导作用域的正确性。
func TestScopeFromRealUser(t *testing.T) {
	normal := &auth.User{TenantID: 5, Roles: []string{auth.RoleUser}}
	if scope.ForUser(normal).CrossTenant {
		t.Fatal("❌ 普通用户不应跨租户")
	}
	admin := &auth.User{TenantID: 5, Roles: []string{auth.RoleAdmin}}
	if scope.ForUser(admin).CrossTenant {
		t.Fatal("❌ 租户内 admin 不应跨租户")
	}
	super := &auth.User{TenantID: 5, Roles: []string{auth.RoleSuperAdmin}}
	if !scope.ForUser(super).CrossTenant {
		t.Fatal("❌ superadmin 应可跨租户")
	}
	t.Log("✅ 角色→作用域映射正确")
}

// TestAgentRegisterTenantIsolation 验证 Agent 注册遵守 enroll token 的租户：
// 1) 令牌所属租户会写入 hosts.tenant_id，并把 agents.host_id 绑定到该主机；
// 2) 同一 IP 已被其他租户占用时拒绝注册，不能劫持对方主机的 agent_id。
func TestAgentRegisterTenantIsolation(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.Config = config.Default()
	sc.Config.Agent.RequireEnrollToken = true

	for _, id := range []int{901, 902} {
		if _, err := db.Exec(`INSERT INTO tenants (id, name) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`,
			id, fmt.Sprintf("iso_reg_%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM tenants WHERE id IN (901,902)`) })

	sfx := time.Now().UnixNano()
	ip := fmt.Sprintf("203.0.113.%d", sfx%250+1)
	tokenB := fmt.Sprintf("tenant-b-token-%d", sfx)
	tokenA := fmt.Sprintf("tenant-a-token-%d", sfx)
	t.Cleanup(func() {
		db.Exec(`DELETE FROM agents WHERE ip=$1`, ip)
		db.Exec(`DELETE FROM hosts WHERE ip=$1`, ip)
		db.Exec(`DELETE FROM enroll_tokens WHERE token_hash=ANY($1)`, pq.Array([]string{
			agentgw.HashToken(tokenB), agentgw.HashToken(tokenA),
		}))
	})

	insertToken := func(token string, tenantID int64) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO enroll_tokens (token_hash, expires_at, created_by, tenant_id)
			VALUES ($1, NOW() + INTERVAL '10 minutes', 'test', $2)`,
			agentgw.HashToken(token), tenantID); err != nil {
			t.Fatal(err)
		}
	}
	insertToken(tokenB, 902)

	post := func(token string) *httptest.ResponseRecorder {
		body := `{"hostname":"iso-reg","arch":"amd64","enroll_token":"` + token + `"}`
		req := httptest.NewRequest("POST", "/api/agent/register", strings.NewReader(body))
		req.RemoteAddr = ip + ":12345"
		rec := httptest.NewRecorder()
		AgentRegister(sc)(rec, req)
		return rec
	}

	rec := post(tokenB)
	if rec.Code != http.StatusOK {
		t.Fatalf("租户 B 注册失败 %d: %s", rec.Code, rec.Body.String())
	}
	var hostID, tenantID int64
	var agentID string
	if err := db.QueryRow(`SELECT id, tenant_id, agent_id FROM hosts WHERE ip=$1`, ip).
		Scan(&hostID, &tenantID, &agentID); err != nil {
		t.Fatal(err)
	}
	if tenantID != 902 {
		t.Fatalf("hosts.tenant_id = %d, want 902", tenantID)
	}
	var boundHostID sql.NullInt64
	if err := db.QueryRow(`SELECT host_id FROM agents WHERE agent_id=$1`, agentID).Scan(&boundHostID); err != nil {
		t.Fatal(err)
	}
	if !boundHostID.Valid || boundHostID.Int64 != hostID {
		t.Fatalf("agents.host_id = %+v, want %d", boundHostID, hostID)
	}

	// 租户 A 的令牌不能复用同一 IP 劫持租户 B 的主机。
	insertToken(tokenA, 901)
	rec = post(tokenA)
	if rec.Code != http.StatusConflict {
		t.Fatalf("跨租户注册应返回 409，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var gotTenant int64
	var gotAgentID string
	db.QueryRow(`SELECT tenant_id, agent_id FROM hosts WHERE ip=$1`, ip).Scan(&gotTenant, &gotAgentID)
	if gotTenant != 902 || gotAgentID != agentID {
		t.Fatalf("跨租户注册改写了主机: tenant=%d agent=%s", gotTenant, gotAgentID)
	}
	// 失败的注册不能消耗令牌。
	var used sql.NullTime
	if err := db.QueryRow(`SELECT used_at FROM enroll_tokens WHERE token_hash=$1`,
		agentgw.HashToken(tokenA)).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used.Valid {
		t.Fatal("注册失败后令牌不应被消费")
	}
}

// TestUserFieldApisTenantIsolation 用户扩展字段以 username 为键、表不带 tenant_id：
// 租户管理员不能读写其他租户用户的字段，列表接口也不能返回其他租户的值。
func TestUserFieldApisTenantIsolation(t *testing.T) {
	db, sc := setupHostDB(t)
	sc.RBAC = rbac.NewService(db)
	sfx := time.Now().UnixNano()
	userA := fmt.Sprintf("uf_a_%d", sfx)
	userB := fmt.Sprintf("uf_b_%d", sfx)

	for _, id := range []int{901, 902} {
		if _, err := db.Exec(`INSERT INTO tenants (id, name) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`,
			id, fmt.Sprintf("uf_tenant_%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM tenants WHERE id IN (901,902)`) })

	for _, u := range []struct {
		name     string
		tenantID int64
	}{{userA, 901}, {userB, 902}} {
		if _, err := db.Exec(`INSERT INTO local_users (username, password_hash, display_name, is_admin, tenant_id, roles)
			VALUES ($1,'x',$1,FALSE,$2,ARRAY['user'])`, u.name, u.tenantID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM user_field_values WHERE user_id = ANY($1)`, pq.Array([]string{userA, userB}))
		db.Exec(`DELETE FROM local_users WHERE username = ANY($1)`, pq.Array([]string{userA, userB}))
	})
	if _, err := db.Exec(`INSERT INTO user_field_values (user_id, field_key, value)
		VALUES ($1,'empCode','A-001'), ($2,'empCode','B-002')`, userA, userB); err != nil {
		t.Fatal(err)
	}

	adminA := &auth.User{ID: "uf-admin", Username: "uf-admin", TenantID: 901, IsAdmin: true, Roles: []string{auth.RoleAdmin}}

	// 跨租户读取：404
	req := httptest.NewRequest("GET", "/api/users/"+userB+"/fields", nil)
	req.SetPathValue("username", userB)
	req = req.WithContext(auth.WithUser(req.Context(), adminA))
	rec := httptest.NewRecorder()
	GetUserFields(sc)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨租户读取字段应 404，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 跨租户写入：404，且原值不变
	req = httptest.NewRequest("PUT", "/api/users/"+userB+"/fields", strings.NewReader(`{"values":{"empCode":"HACKED"}}`))
	req.SetPathValue("username", userB)
	req = req.WithContext(auth.WithUser(req.Context(), adminA))
	rec = httptest.NewRecorder()
	SaveUserFields(sc)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨租户写入字段应 404，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var got string
	if err := db.QueryRow(`SELECT value FROM user_field_values WHERE user_id=$1 AND field_key='empCode'`, userB).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "B-002" {
		t.Fatalf("跨租户写入修改了他人字段: %q", got)
	}

	// 列表：只返回本租户用户与值
	req = httptest.NewRequest("GET", "/api/users/fields", nil)
	req = req.WithContext(auth.WithUser(req.Context(), adminA))
	rec = httptest.NewRecorder()
	ListUserFieldsWithValues(sc)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表失败 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, userB) || strings.Contains(body, "B-002") {
		t.Fatalf("列表泄露了其他租户的用户字段: %s", body)
	}
	if !strings.Contains(body, userA) || !strings.Contains(body, "A-001") {
		t.Fatalf("列表缺少本租户用户字段: %s", body)
	}
}
