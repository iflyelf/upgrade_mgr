package component

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
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
	return db
}

// TestDeleteReferencedComponent 验证被任务引用的组件删除时返回明确提示，
// 而不是把外键错误脱敏成“数据库操作失败”。
func TestDeleteReferencedComponent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	sc := scope.New(1, false)
	repo := NewRepo(db)
	compID, taskID := seedComponentWithTask(t, db, repo, sc, "draft")

	// 被任务引用：返回可读原因，且组件仍然存在。
	err := repo.Delete(ctx, sc, compID)
	if err == nil {
		t.Fatal("期望删除失败（任务引用），实际成功")
	}
	if !strings.Contains(err.Error(), "任务") {
		t.Fatalf("错误信息应提示任务引用，实际: %v", err)
	}
	if _, getErr := repo.Get(ctx, sc, compID); getErr != nil {
		t.Fatalf("组件不应被删除，Get 失败: %v", getErr)
	}
	_ = taskID
}

// TestDeleteWithTasks 验证连带删除：草稿/终态任务一并删除，运行中任务拒绝。
func TestDeleteWithTasks(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	sc := scope.New(1, false)
	repo := NewRepo(db)

	t.Run("draft 任务可连带删除", func(t *testing.T) {
		compID, _ := seedComponentWithTask(t, db, repo, sc, "draft")
		plan, err := repo.CheckDelete(ctx, sc, compID, true)
		if err != nil {
			t.Fatalf("预检失败: %v", err)
		}
		if plan.TaskCount != 1 || plan.BlockingTasks != 0 {
			t.Fatalf("引用统计不符: %+v", plan)
		}
		deleted, err := repo.DeleteWithTasks(ctx, sc, compID)
		if err != nil {
			t.Fatalf("连带删除失败: %v", err)
		}
		if deleted != 1 {
			t.Fatalf("应删除 1 个任务，实际 %d", deleted)
		}
		if _, err := repo.Get(ctx, sc, compID); err == nil {
			t.Fatal("组件应已删除")
		}
	})

	t.Run("running 任务拒绝连带删除", func(t *testing.T) {
		compID, _ := seedComponentWithTask(t, db, repo, sc, "running")
		if _, err := repo.DeleteWithTasks(ctx, sc, compID); err == nil {
			t.Fatal("running 任务不应允许连带删除")
		} else if !strings.Contains(err.Error(), "取消") {
			t.Fatalf("错误信息应提示先取消任务: %v", err)
		}
	})
}

// seedComponentWithTask 建一个组件 + 一个指定状态的任务，并注册清理。
func seedComponentWithTask(t *testing.T, db *sql.DB, repo *Repo, sc scope.Scope, status string) (compID, taskID int64) {
	t.Helper()
	ctx := context.Background()
	name := "delete_ref_" + time.Now().Format("150405.000000000")
	compID, err := repo.Create(ctx, sc, &Component{Name: name, DeployMode: "exec"})
	if err != nil {
		t.Fatalf("建组件失败: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status) VALUES ($1,$2,'latest',$3) RETURNING id`,
		"delete-ref-task", compID, status).Scan(&taskID); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE component_id=$1`, compID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
	})
	return compID, taskID
}
