package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
)

// TestCleanupAuditLogs 按保留天数清理审计日志，未到期记录保留。
func TestCleanupAuditLogs(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	action := fmt.Sprintf("audit-cleanup-%d", time.Now().UnixNano())

	insert := func(at time.Time) int64 {
		var id int64
		if err := db.QueryRow(`
			INSERT INTO audit_logs (username, action, status, created_at, tenant_id)
			VALUES ('tester',$1,'success',$2,1) RETURNING id`, action, at).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldID := insert(time.Now().Add(-40 * 24 * time.Hour))
	newID := insert(time.Now())
	t.Cleanup(func() { db.Exec(`DELETE FROM audit_logs WHERE id IN ($1,$2)`, oldID, newID) })

	m := New(db)
	m.SetAuditRetention(func() int { return 30 })
	m.cleanupAuditLogs(ctx)

	var oldCount, newCount int
	db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE id=$1`, oldID).Scan(&oldCount)
	db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE id=$1`, newID).Scan(&newCount)
	if oldCount != 0 {
		t.Fatal("超期审计日志应被清理")
	}
	if newCount != 1 {
		t.Fatal("未到期审计日志不应被清理")
	}
}
