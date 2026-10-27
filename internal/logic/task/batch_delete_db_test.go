package task

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

// TestBatchDeleteSetBased 按集合删除时逐个任务的判定与单个 Delete 一致：
//   - 草稿/终态且无未完成指令 → 删除，子表级联清理；
//   - 运行中 → 拒绝；
//   - 终态但仍有未完成指令 → 拒绝；
//   - 其他租户 / 不存在 → 视为不存在。
func TestBatchDeleteSetBased(t *testing.T) {
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
	ctx := context.Background()
	sfx := time.Now().UnixNano()

	newTask := func(status string, tenant int64) int64 {
		var id int64
		if err := db.QueryRow(`
			INSERT INTO upgrade_tasks (name, version, kind, status, created_by, updated_by, tenant_id)
			VALUES ($1,'v1','command',$2,'admin','admin',$3) RETURNING id`,
			fmt.Sprintf("bd_%s_%d", status, sfx), status, tenant).Scan(&id); err != nil {
			t.Fatalf("建任务失败: %v", err)
		}
		t.Cleanup(func() { db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, id) })
		return id
	}
	// 其他租户：测试库默认只有租户 1，这里临时建一个。
	var tenant2 int64
	if err := db.QueryRow(`INSERT INTO tenants (name, display_name) VALUES ($1,'批量删除测试') RETURNING id`,
		fmt.Sprintf("bd_tenant_%d", sfx)).Scan(&tenant2); err != nil {
		t.Fatalf("建租户失败: %v", err)
	}
	// Cleanup 为 LIFO：先注册租户清理，保证任务先于租户删除。
	t.Cleanup(func() { db.Exec(`DELETE FROM tenants WHERE id=$1`, tenant2) })

	draft := newTask("draft", 1)
	success := newTask("success", 1)
	running := newTask("running", 1)
	busy := newTask("failed", 1)
	otherTenant := newTask("success", tenant2)

	// success 任务挂一个批次与一条已结束指令：删除后应被级联清理。
	if _, err := db.Exec(`INSERT INTO task_batches (task_id, batch_index, status, total)
		VALUES ($1,0,'success',1)`, success); err != nil {
		t.Fatalf("建批次失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO commands (task_id, agent_id, type, status)
		VALUES ($1,'bd-agent','run_command','success')`, success); err != nil {
		t.Fatalf("建指令失败: %v", err)
	}
	// busy 任务虽已失败，但仍有执行中指令。
	if _, err := db.Exec(`INSERT INTO commands (task_id, agent_id, type, status)
		VALUES ($1,'bd-agent','run_command','running')`, busy); err != nil {
		t.Fatalf("建指令失败: %v", err)
	}

	missing := int64(1 << 60)
	res, err := NewService(db, nil, nil, nil).BatchDelete(ctx, scope.New(1, false),
		[]int64{draft, success, running, busy, otherTenant, missing, draft})
	if err != nil {
		t.Fatalf("批量删除失败: %v", err)
	}

	deleted := map[int64]bool{}
	for _, id := range res.Deleted {
		deleted[id] = true
	}
	if len(res.Deleted) != 2 || !deleted[draft] || !deleted[success] {
		t.Fatalf("应只删除 draft 与 success，实际 %v", res.Deleted)
	}
	failed := map[int64]string{}
	for _, f := range res.Failed {
		failed[f.ID] = f.Error
	}
	for _, id := range []int64{running, busy, otherTenant, missing} {
		if failed[id] == "" {
			t.Fatalf("任务 %d 应删除失败，结果 %+v", id, res.Failed)
		}
	}
	if len(res.Failed) != 4 {
		t.Fatalf("重复 ID 不应重复计入失败: %+v", res.Failed)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM upgrade_tasks WHERE id = ANY(ARRAY[$1,$2]::bigint[])`, draft, success).Scan(&n)
	if n != 0 {
		t.Fatalf("已删除任务仍存在 %d 条", n)
	}
	db.QueryRow(`SELECT COUNT(*) FROM commands WHERE task_id=$1`, success).Scan(&n)
	if n != 0 {
		t.Fatalf("子表指令未级联删除: %d", n)
	}
	db.QueryRow(`SELECT COUNT(*) FROM upgrade_tasks WHERE id = ANY(ARRAY[$1,$2,$3]::bigint[])`,
		running, busy, otherTenant).Scan(&n)
	if n != 3 {
		t.Fatalf("不应删除的任务被删除，剩余 %d 条", n)
	}
}
