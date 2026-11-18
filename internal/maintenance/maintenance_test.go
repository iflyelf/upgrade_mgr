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

// TestCleanupEnrollTokens 自动清理已注销/过期/用尽的注册令牌，保留仍有效的令牌。
func TestCleanupEnrollTokens(t *testing.T) {
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
	sfx := fmt.Sprintf("%d", time.Now().UnixNano())
	hashPrefix := "ct-" + sfx
	shortPrefix := hashPrefix
	if len(shortPrefix) > 32 {
		shortPrefix = shortPrefix[:32]
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM enroll_tokens WHERE token_hash LIKE $1`, hashPrefix+"%") })

	insert := func(tag string, enabled bool, maxUses, usedCount int, createdAt time.Time, expiresAt *time.Time, revokedAt *time.Time) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRow(`
			INSERT INTO enroll_tokens
				(token_hash, token_prefix, token_type, enabled, max_uses, used_count,
				 created_at, expires_at, revoked_at, created_by, name, tenant_id)
			VALUES ($1,$2,'automation',$3,$4,$5,$6,$7,$8,'tester',$9,1)
			RETURNING id`,
			hashPrefix+"-"+tag, shortPrefix, enabled, maxUses, usedCount,
			createdAt, expiresAt, revokedAt, "tester").Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	recent := time.Now().Add(-24 * time.Hour)
	expiredAt := old
	revokedOld := old
	revokedRecent := recent

	oldRevoked := insert("old-revoked", false, 0, 0, old, nil, &revokedOld)
	recentRevoked := insert("recent-revoked", false, 0, 0, recent, nil, &revokedRecent)
	oldExpired := insert("old-expired", true, 0, 0, old, &expiredAt, nil)
	oldUsed := insert("old-used", true, 1, 1, old, nil, nil)
	activePermanent := insert("active-permanent", true, 0, 0, old, nil, nil)

	m := New(db)
	if n, err := m.CleanupEnrollTokens(ctx); err != nil {
		t.Fatalf("清理失败: %v", err)
	} else if n < 1 {
		t.Fatal("应清理至少一个失效令牌")
	}

	exists := func(id int64) bool {
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM enroll_tokens WHERE id=$1`, id).Scan(&n)
		return n > 0
	}
	for name, id := range map[string]int64{
		"old-revoked": oldRevoked, "old-expired": oldExpired, "old-used": oldUsed,
	} {
		if exists(id) {
			t.Fatalf("%s 应被清理", name)
		}
	}
	for name, id := range map[string]int64{
		"recent-revoked": recentRevoked, "active-permanent": activePermanent,
	} {
		if !exists(id) {
			t.Fatalf("%s 不应被清理", name)
		}
	}
}
