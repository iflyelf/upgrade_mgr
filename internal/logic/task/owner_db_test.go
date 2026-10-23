package task

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/auth"
	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/protocol"
)

// TestTaskOwnerOnly 非管理员只能操作自己创建的任务（开关可控）。
func TestTaskOwnerOnly(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer db.Close()
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	ctx := context.Background()
	sfx := time.Now().UnixNano()

	var taskID int64
	if err := db.QueryRow(`
		INSERT INTO upgrade_tasks (name, version, kind, status, created_by, updated_by, tenant_id)
		VALUES ($1,'v1','command','draft','alice','alice',1) RETURNING id`,
		fmt.Sprintf("owner_task_%d", sfx)).Scan(&taskID); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID) })

	s := NewService(db, nil, nil, nil)
	sc := scope.New(1, false)

	// 未开启策略：任何人都能操作（此处只验证通过 owner 校验）
	if err := s.checkTaskOwner(ctx, sc, taskID); err != nil {
		t.Fatalf("策略未开启时应放行: %v", err)
	}

	// 开启策略
	s.SetOwnerOnlyPolicy(func() bool { return true })

	// 创建者本人
	aliceCtx := auth.WithUser(ctx, &auth.User{Username: "alice", TenantID: 1})
	if err := s.checkTaskOwner(aliceCtx, sc, taskID); err != nil {
		t.Fatalf("创建者应可操作: %v", err)
	}

	// 其他普通用户
	bobCtx := auth.WithUser(ctx, &auth.User{Username: "bob", TenantID: 1})
	if err := s.checkTaskOwner(bobCtx, sc, taskID); err == nil {
		t.Fatal("其他用户不应可操作")
	}

	// 管理员豁免
	adminCtx := auth.WithUser(ctx, &auth.User{Username: "admin", TenantID: 1, IsAdmin: true})
	if err := s.checkTaskOwner(adminCtx, sc, taskID); err != nil {
		t.Fatalf("管理员应豁免: %v", err)
	}
	superCtx := auth.WithUser(ctx, &auth.User{Username: "root", Roles: []string{auth.RoleSuperAdmin}})
	if err := s.checkTaskOwner(superCtx, sc, taskID); err != nil {
		t.Fatalf("superadmin 应豁免: %v", err)
	}

	// 匿名（内部/后台）不拦截
	if err := s.checkTaskOwner(ctx, sc, taskID); err != nil {
		t.Fatalf("匿名不应被拦截: %v", err)
	}
}

// TestCreatedByWritten 创建任务时 created_by/updated_by 均写入。
func TestCreatedByWritten(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sfx := time.Now().UnixNano()

	// ip 需是合法 IP，否则 IP 段选择器无法解析。
	hostIP := fmt.Sprintf("10.%d.%d.%d", (sfx/65536)%256, (sfx/256)%256, sfx%256)
	var hostID int64
	if err := db.QueryRow(`
		INSERT INTO hosts (ip, hostname, arch, os_family, tenant_id)
		VALUES ($1,'owner-host','amd64','linux',1) RETURNING id`,
		hostIP).Scan(&hostID); err != nil {
		t.Fatalf("建主机失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hosts WHERE id=$1`, hostID) })

	s := NewService(db, nil, nil, func() bool { return true })
	id, err := s.Create(ctx, scope.New(1, true), CreateRequest{
		Kind:      "command",
		Name:      "owner-test",
		Command:   protocol.CommandSpec{Script: "echo hi", Timeout: "1m"},
		Selector:  Selector{IPExpr: hostIP},
		CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, id) })

	var created, updated string
	if err := db.QueryRow(`SELECT COALESCE(created_by,''), COALESCE(updated_by,'')
	                       FROM upgrade_tasks WHERE id=$1`, id).Scan(&created, &updated); err != nil {
		t.Fatal(err)
	}
	if created != "alice" || updated != "alice" {
		t.Fatalf("created_by=%q updated_by=%q", created, updated)
	}
	_ = pq.Array
}
