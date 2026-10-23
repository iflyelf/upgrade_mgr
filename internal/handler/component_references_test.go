package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/logic/component"
	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/svc"
)

// TestComponentReferences 验证删除前引用查询接口返回任务明细与阻断信息。
func TestComponentReferences(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过数据库集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("数据库迁移失败: %v", err)
	}
	ctx := auth.WithUser(t.Context(), &auth.User{ID: "1", Username: "tester", TenantID: 1, Roles: []string{auth.RoleAdmin}})
	repo := component.NewRepo(db)
	sc := scope.New(1, false)
	compID, err := repo.Create(ctx, sc, &component.Component{
		Name:       "refs_comp_" + strings.ReplaceAll(t.Name(), "/", "_") + "_" + strconv.FormatInt(time.Now().UnixNano(), 10),
		DeployMode: "exec",
	})
	if err != nil {
		t.Fatalf("建组件失败: %v", err)
	}
	var taskID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status) VALUES ('refs-task',$1,'latest','draft') RETURNING id`,
		compID).Scan(&taskID); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE component_id=$1`, compID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
	})

	s := &svc.ServiceContext{DB: db, Component: repo}
	req := httptest.NewRequest(http.MethodGet, "/api/components/"+strconv.FormatInt(compID, 10)+"/references", nil).WithContext(ctx)
	req.SetPathValue("id", strconv.FormatInt(compID, 10))
	rec := httptest.NewRecorder()
	ComponentReferences(s)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("引用查询失败: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			TaskCount int64 `json:"task_count"`
			Tasks     []struct {
				ID     int64  `json:"id"`
				Status string `json:"status"`
			} `json:"tasks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.TaskCount != 1 || len(resp.Data.Tasks) != 1 || resp.Data.Tasks[0].ID != taskID {
		t.Fatalf("引用结果不符: %+v", resp.Data)
	}
}
