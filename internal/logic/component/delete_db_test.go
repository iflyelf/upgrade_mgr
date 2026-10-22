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

// TestDeleteReferencedComponent 验证被任务引用的组件删除时返回明确提示，
// 而不是把外键错误脱敏成“数据库操作失败”。
func TestDeleteReferencedComponent(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过数据库集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatalf("数据库迁移失败: %v", err)
	}
	ctx := context.Background()
	sc := scope.New(1, false)
	name := "delete_ref_" + time.Now().Format("150405.000000")

	repo := NewRepo(db)
	compID, err := repo.Create(ctx, sc, &Component{Name: name, DeployMode: "exec"})
	if err != nil {
		t.Fatalf("建组件失败: %v", err)
	}
	var taskID int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO upgrade_tasks (name, component_id, version, status) VALUES ($1,$2,'latest','draft') RETURNING id`,
		"delete-ref-task", compID).Scan(&taskID); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM upgrade_tasks WHERE id=$1`, taskID)
		db.Exec(`DELETE FROM components WHERE id=$1`, compID)
	})

	// 被任务引用：返回可读原因，且组件仍然存在。
	err = repo.Delete(ctx, sc, compID)
	if err == nil {
		t.Fatal("期望删除失败（任务引用），实际成功")
	}
	if !strings.Contains(err.Error(), "任务") {
		t.Fatalf("错误信息应提示任务引用，实际: %v", err)
	}
	if _, getErr := repo.Get(ctx, sc, compID); getErr != nil {
		t.Fatalf("组件不应被删除，Get 失败: %v", getErr)
	}

	// 删除任务后可以正常删除组件。
	if _, err := db.ExecContext(ctx, `DELETE FROM upgrade_tasks WHERE id=$1`, taskID); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	if err := repo.Delete(ctx, sc, compID); err != nil {
		t.Fatalf("无引用后删除组件失败: %v", err)
	}
}
