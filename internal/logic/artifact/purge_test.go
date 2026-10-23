package artifact

import (
	"context"
	"database/sql"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/lib/pq"
)

// TestPurgeByComponent 验证：制品文件被真实删除；越界路径被拒绝（不删库外文件）。
func TestPurgeByComponent(t *testing.T) {
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

	root := t.TempDir()
	repo := NewRepo(db, root)

	var compID int64
	if err := db.QueryRow(`INSERT INTO components (name, deploy_mode)
		VALUES ('purge_t_comp','replace') RETURNING id`).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM components WHERE id=$1`, compID)

	// 正常制品：位于 root 内，应被删除
	inside := filepath.Join(root, "1", "v1-amd64", "app.bin")
	os.MkdirAll(filepath.Dir(inside), 0o755)
	os.WriteFile(inside, []byte("x"), 0o644)

	// 越界制品（模拟 DB 被篡改）：位于 root 外，必须保留
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "must_keep.txt")
	os.WriteFile(outside, []byte("keep"), 0o644)

	db.Exec(`INSERT INTO artifacts (component_id,version,arch,filename,md5,path,storage)
		VALUES ($1,'v1','amd64','app.bin','d1',$2,'local')`, compID, inside)
	db.Exec(`INSERT INTO artifacts (component_id,version,arch,filename,md5,path,storage)
		VALUES ($1,'v2','amd64','evil','d2',$2,'local')`, compID, outside)

	if err := repo.PurgeByComponent(context.Background(), scope.New(1, false), compID); err != nil {
		t.Fatalf("purge 失败: %v", err)
	}

	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatal("库内制品文件应被删除")
	}
	t.Log("✅ 库内制品文件已清理")

	if _, err := os.Stat(outside); err != nil {
		t.Fatal("越界路径文件必须保留（防越权删除），但已丢失")
	}
	t.Log("✅ 越界路径被拒绝，库外文件未被删除")

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE component_id=$1`, compID).Scan(&n)
	if n != 0 {
		t.Fatalf("制品元数据应清空，剩余 %d", n)
	}
	t.Log("✅ 制品元数据已清理")
}
