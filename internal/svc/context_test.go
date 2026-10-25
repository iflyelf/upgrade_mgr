package svc

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/config"
	"github.com/iflyelf/upgrade_mgr/internal/logic/artifact"
	"github.com/iflyelf/upgrade_mgr/internal/setting"
)

// TestEnsureSessionSecretPersists 验证空 SESSION_SECRET 生成一次并持久化，
// 第二个副本/下一次启动读到同一值；显式长密钥不被覆盖。
//
// 使用独立 schema，避免影响 public.app_settings。
func TestEnsureSessionSecretPersists(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过会话密钥集成测试")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	// 关闭必须在其他 t.Cleanup（删测试数据）之后：LIFO 顺序。
	t.Cleanup(func() { db.Close() })
	// 单连接保证 SET search_path 对后续 ensureSessionSecret 生效。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	schema := fmt.Sprintf("svc_secret_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("创建 schema 失败: %v", err)
	}
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatalf("设置 search_path 失败: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE app_settings (
			key VARCHAR(128) PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`); err != nil {
		t.Fatalf("创建 app_settings 失败: %v", err)
	}

	c1 := &config.Config{}
	ensureSessionSecret(db, c1)
	if len(c1.Server.SessionSecret) < 16 {
		t.Fatalf("自动生成密钥失败，长度 %d", len(c1.Server.SessionSecret))
	}

	c2 := &config.Config{}
	ensureSessionSecret(db, c2)
	if c2.Server.SessionSecret != c1.Server.SessionSecret {
		t.Fatal("第二个实例未读到同一会话密钥")
	}

	// 过短的显式密钥应回退到数据库共享密钥。
	c3 := &config.Config{}
	c3.Server.SessionSecret = "short"
	ensureSessionSecret(db, c3)
	if c3.Server.SessionSecret != c1.Server.SessionSecret {
		t.Fatal("过短密钥未回退到数据库密钥")
	}

	// 显式 32+ 字节密钥优先，不应被数据库值覆盖。
	explicit := "explicit-session-secret-32-bytes!"
	c4 := &config.Config{}
	c4.Server.SessionSecret = explicit
	ensureSessionSecret(db, c4)
	if c4.Server.SessionSecret != explicit {
		t.Fatal("显式密钥被数据库值覆盖")
	}
}

// TestTuneDBPoolForArtifactUploads 连接池下限必须覆盖并发制品上传。
func TestTuneDBPoolForArtifactUploads(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://invalid:invalid@127.0.0.1:1/nope")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	tuneDBPool(db, 5, 20, 4)
	if got := db.Stats().MaxOpenConnections; got != 8 {
		t.Fatalf("MaxOpenConnections = %d, want 8（上传 4 + 余量 4）", got)
	}

	// 配置足够大时不缩小，空闲连接数不超过最大连接数。
	tuneDBPool(db, 50, 100, 8)
	if got := db.Stats().MaxOpenConnections; got != 50 {
		t.Fatalf("MaxOpenConnections = %d, want 50", got)
	}
}

// TestReloadArtifactSettingsHot 页面保存后调用的热更新入口：
// 上传并发限制与锁等待应无需重启立即改变。
func TestReloadArtifactSettingsHot(t *testing.T) {
	c := config.Default()
	c.Artifact.MaxConcurrentUploads = 2
	c.Artifact.LockWait = "2m"
	repo := artifact.NewRepo(nil, t.TempDir())
	s := &ServiceContext{
		Settings: setting.NewService(nil, c),
		Artifact: repo,
	}
	s.ReloadArtifactSettings()

	release1, err := repo.AcquireUploadSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release1()
	release2, err := repo.AcquireUploadSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release2()

	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := repo.AcquireUploadSlot(short); err == nil {
		t.Fatal("上限为 2 时第三个上传应等待超时")
	}

	// 调大上限后，无需重启即可放行更多上传。
	c.Artifact.MaxConcurrentUploads = 4
	c.Artifact.LockWait = "1m"
	s.ReloadArtifactSettings()
	release3, err := repo.AcquireUploadSlot(context.Background())
	if err != nil {
		t.Fatalf("热更新调大上限后应能继续获取槽位: %v", err)
	}
	release3()
	if got := repo.LockWaitDuration(); got != time.Minute {
		t.Fatalf("锁等待未热更新: %s", got)
	}
}

// openSettingsTestDB 创建独立 schema 的 app_settings，避免影响 public 表。
func openSettingsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过设置同步集成测试")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("svc_watch_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		db.Close()
		admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
	})
	return db
}

// TestArtifactSettingsWatcherSyncsReplica 一个副本保存设置后，另一个副本
// 应通过轮询自动热更新上传并发与锁等待，无需重启。
func TestArtifactSettingsWatcherSyncsReplica(t *testing.T) {
	db := openSettingsTestDB(t)
	cfgA := config.Default()
	cfgB := config.Default()
	settingsA := setting.NewService(db, cfgA)
	settingsB := setting.NewService(db, cfgB)
	repoB := artifact.NewRepo(db, t.TempDir())

	sb := &ServiceContext{
		DB: db, Settings: settingsB, Artifact: repoB,
		dbMaxOpenBase: 5, dbMaxIdleBase: 2,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sb.StartArtifactSettingsWatcher(ctx, 20*time.Millisecond)

	if _, err := settingsA.Apply(context.Background(), map[string]string{
		"artifact.max_concurrent_uploads": "5",
		"artifact.lock_wait":              "45s",
	}); err != nil {
		t.Fatalf("副本 A 保存设置失败: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snap := settingsB.Snapshot()
		if snap.Artifact.MaxConcurrentUploads == 5 &&
			snap.Artifact.LockWait == "45s" &&
			repoB.LockWaitDuration() == 45*time.Second {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	snap := settingsB.Snapshot()
	t.Fatalf("副本 B 未同步：uploads=%d lock_wait=%q repo_wait=%s",
		snap.Artifact.MaxConcurrentUploads, snap.Artifact.LockWait, repoB.LockWaitDuration())
}
