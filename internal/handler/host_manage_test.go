package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

func TestNormalizeHostFields(t *testing.T) {
	name := "  web-01  "
	empty := "   "
	req := &hostUpdateRequest{Hostname: &name, Account: &empty}
	if err := normalizeHostFields(req.fields()); err != nil {
		t.Fatalf("normalizeHostFields: %v", err)
	}
	if *req.Hostname != "web-01" {
		t.Fatalf("hostname 未去空白: %q", *req.Hostname)
	}
	if *req.Account != "" {
		t.Fatalf("空白字段应被清空，实际 %q", *req.Account)
	}

	long := strings.Repeat("x", 256)
	if err := normalizeHostFields((&hostUpdateRequest{Hostname: &long}).fields()); err == nil {
		t.Fatal("超长 hostname 应报错")
	}

	ctrl := "web\n01"
	if err := normalizeHostFields((&hostUpdateRequest{Hostname: &ctrl}).fields()); err == nil {
		t.Fatal("控制字符应报错")
	}

	// nil 表示不修改，不能被当成空字符串。
	if err := normalizeHostFields((&hostUpdateRequest{}).fields()); err != nil {
		t.Fatalf("nil 字段不应报错: %v", err)
	}
}

// setupHostDB 连接测试库并自动迁移；未设置 DATABASE_URL 时跳过。
func setupHostDB(t *testing.T) (*sql.DB, *svc.ServiceContext) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过主机管理集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db, &svc.ServiceContext{DB: db}
}

func hostTestUser(tenantID int64) *auth.User {
	return &auth.User{
		ID: "test-user", Username: "tester", IsAdmin: true,
		TenantID: tenantID, Roles: []string{auth.RoleAdmin},
	}
}

func insertTestHost(t *testing.T, db *sql.DB, tenantID int64) (int64, string) {
	t.Helper()
	ip := fmt.Sprintf("host-test-%d", time.Now().UnixNano())
	var id int64
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, account, app_code, tenant_id, source)
		VALUES ($1, 'orig', 'keep-me', 'OLD', $2, 'manual')
		RETURNING id`, ip, tenantID).Scan(&id); err != nil {
		t.Fatalf("插入测试主机失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE id=$1`, id) })
	return id, ip
}

func callHostHandler(t *testing.T, h http.HandlerFunc, method, id, body string, u *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/hosts/"+id, strings.NewReader(body))
	req.SetPathValue("id", id)
	req = req.WithContext(auth.WithUser(req.Context(), u))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestListHostsPagination(t *testing.T) {
	db, sc := setupHostDB(t)
	prefix := fmt.Sprintf("page-test-%d", time.Now().UnixNano())
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(`
			INSERT INTO hosts (ip, hostname, tenant_id, source)
			VALUES ($1, $2, 1, 'manual')`,
			fmt.Sprintf("%s-%d", prefix, i), prefix); err != nil {
			t.Fatalf("插入分页测试主机失败: %v", err)
		}
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE ip LIKE $1`, prefix+"%") })

	type hostPage struct {
		Code int `json:"code"`
		Data struct {
			Total int    `json:"total"`
			Page  int    `json:"page"`
			Size  int    `json:"size"`
			Pages int    `json:"pages"`
			List  []Host `json:"list"`
		} `json:"data"`
	}
	query := func(page int) hostPage {
		req := httptest.NewRequest("GET",
			fmt.Sprintf("/api/hosts?q=%s&page=%d&size=2", prefix, page), nil)
		req = req.WithContext(auth.WithUser(req.Context(), hostTestUser(1)))
		rec := httptest.NewRecorder()
		ListHosts(sc)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("分页查询失败 %d: %s", rec.Code, rec.Body.String())
		}
		var out hostPage
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析分页响应失败: %v", err)
		}
		return out
	}

	got := query(2)
	if got.Data.Total != 5 || got.Data.Page != 2 || got.Data.Size != 2 ||
		got.Data.Pages != 3 || len(got.Data.List) != 2 {
		t.Fatalf("第 2 页结果不符: %+v", got.Data)
	}

	// 页码越界应自动落到最后一页，而不是返回空列表。
	got = query(99)
	if got.Data.Page != 3 || len(got.Data.List) != 1 {
		t.Fatalf("越界页码应落到最后一页: %+v", got.Data)
	}
}

func TestHostUpdateTenantIsolation(t *testing.T) {
	db, sc := setupHostDB(t)
	hostID, _ := insertTestHost(t, db, 1)
	idStr := strconv.FormatInt(hostID, 10)

	rec := callHostHandler(t, HostUpdate(sc), http.MethodPut, idStr,
		`{"hostname":"  web-01  ","app_code":" APP "}`, hostTestUser(1))
	if rec.Code != http.StatusOK {
		t.Fatalf("本租户更新应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var hostname, account, appCode string
	if err := db.QueryRow(`SELECT hostname, account, app_code FROM hosts WHERE id=$1`, hostID).
		Scan(&hostname, &account, &appCode); err != nil {
		t.Fatal(err)
	}
	if hostname != "web-01" || appCode != "APP" {
		t.Fatalf("字段更新/去空白失败: hostname=%q app_code=%q", hostname, appCode)
	}
	if account != "keep-me" {
		t.Fatalf("未提交字段不应被清空，account=%q", account)
	}

	// 跨租户更新必须 404，且数据不变。
	rec = callHostHandler(t, HostUpdate(sc), http.MethodPut, idStr,
		`{"hostname":"hacked"}`, hostTestUser(9902))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨租户更新应 404，实际 %d", rec.Code)
	}
	db.QueryRow(`SELECT hostname FROM hosts WHERE id=$1`, hostID).Scan(&hostname)
	if hostname != "web-01" {
		t.Fatalf("跨租户更新篡改了数据: %q", hostname)
	}
}

func TestHostDeleteTenantIsolationAndAgentRevoke(t *testing.T) {
	db, sc := setupHostDB(t)
	hostID, _ := insertTestHost(t, db, 1)
	idStr := strconv.FormatInt(hostID, 10)
	agentID := fmt.Sprintf("agent-test-%d", time.Now().UnixNano())

	// 模拟真实注册：hosts.agent_id 有值，但 agents.host_id 仍为 NULL。
	if _, err := db.Exec(`UPDATE hosts SET agent_id=$1 WHERE id=$2`, agentID, hostID); err != nil {
		t.Fatalf("回填 hosts.agent_id 失败: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO agents (agent_id, host_id, ip, token_hash, secret, status)
		VALUES ($1, NULL, '127.0.0.1', 'hash', 'secret', 'online')`, agentID); err != nil {
		t.Fatalf("插入测试 Agent 失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM agents WHERE agent_id=$1`, agentID) })

	// 跨租户删除必须 404，主机和 Agent 都不能被删。
	rec := callHostHandler(t, HostDelete(sc), http.MethodDelete, idStr, "", hostTestUser(9902))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨租户删除应 404，实际 %d", rec.Code)
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(1) FROM hosts WHERE id=$1`, hostID).Scan(&cnt)
	if cnt != 1 {
		t.Fatal("跨租户删除不应删除主机")
	}

	// 本租户删除成功，并撤销 Agent 凭据。
	rec = callHostHandler(t, HostDelete(sc), http.MethodDelete, idStr, "", hostTestUser(1))
	if rec.Code != http.StatusOK {
		t.Fatalf("本租户删除应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	db.QueryRow(`SELECT COUNT(1) FROM hosts WHERE id=$1`, hostID).Scan(&cnt)
	if cnt != 0 {
		t.Fatal("主机未被删除")
	}
	db.QueryRow(`SELECT COUNT(1) FROM agents WHERE agent_id=$1`, agentID).Scan(&cnt)
	if cnt != 0 {
		t.Fatal("Agent 凭据未被撤销")
	}
}

func TestHostDeleteRejectsActiveTask(t *testing.T) {
	db, sc := setupHostDB(t)
	hostID, _ := insertTestHost(t, db, 1)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	var compID, taskID int64
	if err := db.QueryRow(`
		INSERT INTO components (tenant_id, name, deploy_mode)
		VALUES (1, $1, 'replace') RETURNING id`, "host-test-comp-"+suffix).Scan(&compID); err != nil {
		t.Fatalf("插入测试组件失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM components WHERE id=$1`, compID) })

	if err := db.QueryRow(`
		INSERT INTO upgrade_tasks (tenant_id, name, component_id, version, status)
		VALUES (1, $1, $2, '1.0.0', 'running') RETURNING id`,
		"host-test-task-"+suffix, compID).Scan(&taskID); err != nil {
		t.Fatalf("插入测试任务失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID) })

	if _, err := db.Exec(`
		INSERT INTO task_hosts (task_id, host_id, status) VALUES ($1, $2, 'pending')`,
		taskID, hostID); err != nil {
		t.Fatalf("插入 task_host 失败: %v", err)
	}

	rec := callHostHandler(t, HostDelete(sc), http.MethodDelete,
		strconv.FormatInt(hostID, 10), "", hostTestUser(1))
	if rec.Code != http.StatusConflict {
		t.Fatalf("存在未完成任务时应 409，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(1) FROM hosts WHERE id=$1`, hostID).Scan(&cnt)
	if cnt != 1 {
		t.Fatal("被拒绝的删除不应删除主机")
	}
}
