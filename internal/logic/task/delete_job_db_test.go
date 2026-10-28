package task

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// TestDeleteJobLifecycle 异步删除作业：
//   - 创建后立即返回 pending；同一发起人不能同时创建第二个；
//   - 后台按批执行，可删的删除、不可删的记入失败；
//   - 执行结束状态为 done，进度与结果持久化；
//   - 停止请求让未处理完的作业以 canceled 结束。
func TestDeleteJobLifecycle(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过数据库集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("数据库迁移失败: %v", err)
	}
	sfx := time.Now().UnixNano()
	user := fmt.Sprintf("job-admin-%d", sfx)
	ctx := auth.WithUser(context.Background(), &auth.User{
		ID: user, Username: user, IsAdmin: true, TenantID: 1, Roles: []string{auth.RoleAdmin},
	})
	sc := scope.New(1, false)
	t.Cleanup(func() { db.Exec(`DELETE FROM task_delete_jobs WHERE created_by=$1`, user) })

	newTask := func(status string) int64 {
		var id int64
		if err := db.QueryRow(`
			INSERT INTO upgrade_tasks (name, version, kind, status, created_by, updated_by, tenant_id)
			VALUES ($1,'v1','command',$2,$3,$3,1) RETURNING id`,
			fmt.Sprintf("job_%s_%d", status, sfx), status, user).Scan(&id); err != nil {
			t.Fatalf("建任务失败: %v", err)
		}
		t.Cleanup(func() { db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, id) })
		return id
	}
	a, b, running := newTask("success"), newTask("failed"), newTask("running")

	s := NewService(db, nil, nil, nil)
	job, err := s.CreateDeleteJob(ctx, sc, "127.0.0.1", []int64{a, b, running, a})
	if err != nil {
		t.Fatalf("创建作业失败: %v", err)
	}
	if job.Status != "pending" || job.Total != 3 {
		t.Fatalf("新作业应为 pending 且去重后 3 个: %+v", job)
	}
	if _, err := s.CreateDeleteJob(ctx, sc, "", []int64{a}); err == nil {
		t.Fatal("已有未结束作业时不应允许再创建")
	}
	if active, err := s.ActiveDeleteJob(ctx, sc); err != nil || active == nil || active.ID != job.ID {
		t.Fatalf("应能查到未结束作业: %+v %v", active, err)
	}

	// 驱动后台执行直到作业结束（其他测试残留的作业也会被顺带处理）。
	for i := 0; i < 50; i++ {
		did, err := s.runDeleteJobOnce(context.Background())
		if err != nil {
			t.Fatalf("执行作业失败: %v", err)
		}
		if !did {
			break
		}
	}
	got, err := s.GetDeleteJob(ctx, sc, job.ID)
	if err != nil {
		t.Fatalf("查询作业失败: %v", err)
	}
	if got.Status != "done" || got.Processed != 3 || got.Deleted != 2 || got.FailedCount != 1 {
		t.Fatalf("作业结果不符: %+v", got)
	}
	if len(got.Failed) != 1 || got.Failed[0].ID != running {
		t.Fatalf("运行中任务应记入失败: %+v", got.Failed)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM upgrade_tasks WHERE id = ANY(ARRAY[$1,$2]::bigint[])`, a, b).Scan(&n)
	if n != 0 {
		t.Fatalf("可删除任务未删除: 剩余 %d", n)
	}

	// 停止：创建后立即请求停止，作业以 canceled 结束且不删除任务。
	c := newTask("success")
	job2, err := s.CreateDeleteJob(ctx, sc, "", []int64{c})
	if err != nil {
		t.Fatalf("创建第二个作业失败: %v", err)
	}
	if _, err := s.CancelDeleteJob(ctx, sc, job2.ID); err != nil {
		t.Fatalf("停止作业失败: %v", err)
	}
	for i := 0; i < 50; i++ {
		did, err := s.runDeleteJobOnce(context.Background())
		if err != nil {
			t.Fatalf("执行作业失败: %v", err)
		}
		if !did {
			break
		}
	}
	got2, _ := s.GetDeleteJob(ctx, sc, job2.ID)
	if got2 == nil || got2.Status != "canceled" {
		t.Fatalf("停止后应为 canceled: %+v", got2)
	}
	db.QueryRow(`SELECT COUNT(*) FROM upgrade_tasks WHERE id=$1`, c).Scan(&n)
	if n != 1 {
		t.Fatal("停止的作业不应删除任务")
	}
}
