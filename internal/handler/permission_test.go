package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/task"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestMyPermissions 管理员返回 admin=true；普通用户返回团队授权明细。
func TestMyPermissions(t *testing.T) {
	db, base := setupHostDB(t)
	sfx := time.Now().UnixNano()
	compName := fmt.Sprintf("perm-comp-%d", sfx)

	var compID int64
	if err := db.QueryRow(`
		INSERT INTO components (name, display_name, deploy_mode)
		VALUES ($1,'权限测试组件','replace') RETURNING id`, compName).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM components WHERE id=$1`, compID) })

	var groupID int64
	if err := db.QueryRow(`INSERT INTO service_groups (name) VALUES ($1) RETURNING id`,
		fmt.Sprintf("perm-group-%d", sfx)).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM service_groups WHERE id=$1`, groupID) })
	if _, err := db.Exec(`INSERT INTO service_group_components (group_id, component_id) VALUES ($1,$2)`,
		groupID, compID); err != nil {
		t.Fatal(err)
	}

	var teamID int64
	if err := db.QueryRow(`INSERT INTO teams (name) VALUES ($1) RETURNING id`,
		fmt.Sprintf("perm-team-%d", sfx)).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM teams WHERE id=$1`, teamID) })
	if _, err := db.Exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1,'alice')`, teamID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO team_group_permissions (team_id, group_id, permissions, services)
		VALUES ($1,$2,ARRAY['deploy'],ARRAY['*'])`, teamID, groupID); err != nil {
		t.Fatal(err)
	}

	sc := &svc.ServiceContext{DB: db, Task: task.NewService(db, nil, nil, nil)}
	_ = base

	call := func(u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/my/permissions", nil)
		req = req.WithContext(auth.WithUser(req.Context(), u))
		rec := httptest.NewRecorder()
		MyPermissions(sc)(rec, req)
		return rec
	}

	// 普通用户 alice：应有组件 deploy 权限
	rec := call(&auth.User{Username: "alice", TenantID: 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("查询失败 %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Admin      bool                       `json:"admin"`
			Components map[string]map[string]bool `json:"components"`
			HostGroups map[string]map[string]bool `json:"host_groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Admin {
		t.Fatal("普通用户不应是 admin")
	}
	if !out.Data.Components[compName]["deploy"] {
		t.Fatalf("应包含组件 %s 的 deploy 权限: %+v", compName, out.Data.Components)
	}

	// 管理员：admin=true
	rec = call(hostTestUser(1))
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Data.Admin {
		t.Fatal("管理员应返回 admin=true")
	}
}

// TestTaskListVisibility 非管理员看不到命令任务；owner 策略为 on 时仅本人任务可操作。
func TestTaskListVisibility(t *testing.T) {
	db, _ := setupHostDB(t)
	sfx := time.Now().UnixNano()
	sc := &svc.ServiceContext{DB: db, Task: task.NewService(db, nil, nil, nil)}
	sc.Task.SetOwnerOnlyPolicy(func() bool { return true })

	insert := func(name, kind, createdBy string) int64 {
		var id int64
		if err := db.QueryRow(`
			INSERT INTO upgrade_tasks (name, version, kind, status, created_by, updated_by, tenant_id)
			VALUES ($1,'v1',$2,'draft',$3,$3,1) RETURNING id`, name, kind, createdBy).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	aliceTask := insert(fmt.Sprintf("vis-alice-%d", sfx), "upgrade", "alice")
	bobTask := insert(fmt.Sprintf("vis-bob-%d", sfx), "upgrade", "bob")
	cmdTask := insert(fmt.Sprintf("vis-cmd-%d", sfx), "command", "alice")
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id = ANY($1)`,
			[]int64{aliceTask, bobTask, cmdTask})
	})

	list := func(u *auth.User) []struct {
		ID         int64  `json:"id"`
		Kind       string `json:"kind"`
		CreatedBy  string `json:"created_by"`
		CanOperate bool   `json:"can_operate"`
	} {
		req := httptest.NewRequest("GET", "/api/tasks", nil)
		req = req.WithContext(auth.WithUser(req.Context(), u))
		rec := httptest.NewRecorder()
		TaskList(sc)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("列表失败 %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data []struct {
				ID         int64  `json:"id"`
				Kind       string `json:"kind"`
				CreatedBy  string `json:"created_by"`
				CanOperate bool   `json:"can_operate"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data
	}

	for _, row := range list(&auth.User{Username: "alice", TenantID: 1}) {
		if row.Kind == "command" {
			t.Fatalf("非管理员不应看到命令任务: %+v", row)
		}
		if row.ID == bobTask && row.CanOperate {
			t.Fatal("owner 策略 on：不应可操作他人任务")
		}
		if row.ID == aliceTask && !row.CanOperate {
			t.Fatal("本人任务应可操作")
		}
	}

	seen := map[int64]bool{}
	for _, row := range list(hostTestUser(1)) {
		seen[row.ID] = true
	}
	if !seen[aliceTask] || !seen[bobTask] || !seen[cmdTask] {
		t.Fatalf("管理员应看到全部任务: %v", seen)
	}
}
