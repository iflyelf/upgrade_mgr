package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/iflyelf/upgrade_mgr/internal/migrate"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/pglock"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
)

func openArtifactDB(t *testing.T) *sql.DB {
	t.Helper()
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
	return db
}

// TestSaveTenantIsolation 制品必须归属组件所在租户：
// 租户管理员不能把制品挂到其他租户的组件；superadmin 跨租户上传时
// 制品应落在组件所属租户，保证任务能按租户作用域找到它。
func TestSaveTenantIsolation(t *testing.T) {
	db := openArtifactDB(t)
	ctx := context.Background()
	sfx := time.Now().UnixNano()

	for _, id := range []int{901, 902} {
		if _, err := db.Exec(`INSERT INTO tenants (id,name) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`,
			id, fmt.Sprintf("artifact_tenant_%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM tenants WHERE id IN (901,902)`) })

	newComponent := func(name string, tenantID int64) int64 {
		var id int64
		if err := db.QueryRow(`INSERT INTO components (name, deploy_mode, tenant_id)
			VALUES ($1,'replace',$2) RETURNING id`, name, tenantID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	compA := newComponent(fmt.Sprintf("art_tenant_a_%d", sfx), 901)
	compB := newComponent(fmt.Sprintf("art_tenant_b_%d", sfx), 902)
	t.Cleanup(func() {
		db.Exec(`DELETE FROM components WHERE id IN ($1,$2)`, compA, compB)
	})

	repo := NewRepo(db, t.TempDir())
	save := func(sc scope.Scope, compID int64, version string) (*Artifact, error) {
		return repo.Save(ctx, sc, &Artifact{
			ComponentID: compID, Version: version, Arch: "amd64", Filename: "bin",
		}, bytes.NewReader([]byte("artifact-content")))
	}

	// 租户 A 管理员挂到租户 B 的组件：拒绝
	if _, err := save(scope.New(901, false), compB, fmt.Sprintf("v-%d", sfx)); err == nil {
		t.Fatal("跨租户上传制品必须被拒绝")
	}

	// 本租户上传：成功且归属 A
	gotA, err := save(scope.New(901, false), compA, fmt.Sprintf("v-%d", sfx))
	if err != nil {
		t.Fatalf("本租户上传失败: %v", err)
	}
	if gotA.TenantID != 901 {
		t.Fatalf("制品租户应为 901，实际 %d", gotA.TenantID)
	}

	// superadmin 跨租户上传到 B 的组件：成功且归属 B（不是 superadmin 自己的租户）
	gotB, err := save(scope.New(901, true), compB, fmt.Sprintf("v-%d", sfx))
	if err != nil {
		t.Fatalf("superadmin 跨租户上传失败: %v", err)
	}
	if gotB.TenantID != 902 {
		t.Fatalf("跨租户上传的制品应归属组件租户 902，实际 %d", gotB.TenantID)
	}
}

// TestSaveConcurrentSameKey 同一制品键并发上传后，落盘内容必须与数据库里的
// SHA256 一致（否则 Agent 下载校验会失败）。
func TestSaveConcurrentSameKey(t *testing.T) {
	db := openArtifactDB(t)
	ctx := context.Background()
	sfx := time.Now().UnixNano()
	for _, id := range []int{901, 902} {
		if _, err := db.Exec(`INSERT INTO tenants (id,name) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`,
			id, fmt.Sprintf("artifact_tenant_%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM tenants WHERE id IN (901,902)`) })

	var compID int64
	if err := db.QueryRow(`INSERT INTO components (name, deploy_mode, tenant_id)
		VALUES ($1,'replace',901) RETURNING id`, fmt.Sprintf("art_concurrent_%d", sfx)).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM components WHERE id=$1`, compID) })

	// 两个 Repo 实例模拟两个副本（各自有进程内锁），必须依赖 PG advisory lock 串行化。
	dir := t.TempDir()
	repo := NewRepo(db, dir)
	repo2 := NewRepo(db, dir)
	sc := scope.New(901, false)
	version := fmt.Sprintf("v-concurrent-%d", sfx)
	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	payloads := make(map[string]bool)
	for i := 0; i < writers; i++ {
		payload := []byte(fmt.Sprintf("payload-%d-%d", sfx, i))
		sum := sha256.Sum256(payload)
		payloads[hex.EncodeToString(sum[:])] = true
		wg.Add(1)
		go func(i int, p []byte) {
			defer wg.Done()
			target := repo
			if i%2 == 1 {
				target = repo2
			}
			if _, err := target.Save(ctx, sc, &Artifact{
				ComponentID: compID, Version: version, Arch: "amd64", Filename: "bin",
			}, bytes.NewReader(p)); err != nil {
				errs <- err
			}
		}(i, payload)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发保存失败: %v", err)
	}

	a, err := repo.Find(ctx, sc, compID, version, "amd64", "")
	if err != nil {
		t.Fatal(err)
	}
	f, err := repo.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != a.SHA256 {
		t.Fatalf("文件内容与元数据 SHA256 不一致：file=%s meta=%s",
			hex.EncodeToString(sum[:]), a.SHA256)
	}
	if !payloads[a.SHA256] {
		t.Fatalf("元数据 SHA256 不属于任何一次写入: %s", a.SHA256)
	}
}

// TestArtifactDBLockSerializes 验证 advisory lock 跨连接生效：
// 持锁期间另一条连接/副本无法取得同一 key 的锁，释放后才能继续。
func TestArtifactDBLockSerializes(t *testing.T) {
	db := openArtifactDB(t)
	dsn := os.Getenv("DATABASE_URL")
	db2, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db2.Close() })

	key := pglock.KeyFromString(fmt.Sprintf("artifact-lock-%d", time.Now().UnixNano()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	lock1, err := acquireArtifactDBLock(ctx, db, key, defaultArtifactLockWait)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lock1.Unlock)

	acquired := make(chan error, 1)
	go func() {
		lock2, err := acquireArtifactDBLock(ctx, db2, key, defaultArtifactLockWait)
		if err == nil {
			lock2.Unlock()
		}
		acquired <- err
	}()

	select {
	case err := <-acquired:
		t.Fatalf("首锁未释放时不应取得同一 key 的锁（err=%v）", err)
	case <-time.After(300 * time.Millisecond):
	}
	lock1.Unlock()

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("释放后应能取得锁: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("释放后等待锁超时")
	}
}

// TestPurgeWaitsForComponentLock PurgeByComponent 必须与上传使用同一把
// 组件级锁：持锁期间 purge 不能继续，释放后才能删除。
func TestPurgeWaitsForComponentLock(t *testing.T) {
	db := openArtifactDB(t)
	sfx := time.Now().UnixNano()
	var compID int64
	if err := db.QueryRow(`INSERT INTO components (name, deploy_mode, tenant_id)
		VALUES ($1,'replace',1) RETURNING id`, fmt.Sprintf("art_purge_lock_%d", sfx)).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM components WHERE id=$1`, compID) })

	repo := NewRepo(db, t.TempDir())
	sc := scope.New(1, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := repo.LockComponent(ctx, compID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- repo.PurgeByComponent(ctx, sc, compID) }()
	select {
	case err := <-done:
		t.Fatalf("持锁期间 PurgeByComponent 不应完成（err=%v）", err)
	case <-time.After(300 * time.Millisecond):
	}
	lock.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("释放后 PurgeByComponent 应成功: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("释放后 PurgeByComponent 超时")
	}
}

// TestUploadSlotLimit 上传槽位应限制本实例并发数，context 取消时返回错误。
func TestUploadSlotLimit(t *testing.T) {
	repo := NewRepo(nil, t.TempDir())
	repo.SetUploadConcurrency(1)
	release, err := repo.AcquireUploadSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := repo.AcquireUploadSlot(short); err == nil {
		t.Fatal("槽位已满时应等待并因 context 超时返回错误")
	}
	release()
	release() // 释放函数必须可重复调用
	next, err := repo.AcquireUploadSlot(context.Background())
	if err != nil {
		t.Fatalf("释放后应能再次获取槽位: %v", err)
	}
	next()
}

// blockingReader 第一次 Read 时通知测试，然后阻塞直到 release 关闭。
type blockingReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	data    []byte
	done    bool
}

func (b *blockingReader) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	if !b.done {
		<-b.release
		b.done = true
		n := copy(p, b.data)
		return n, nil
	}
	return 0, io.EOF
}

// TestSaveDifferentArtifactsParallel 同一组件下不同制品必须可以并行上传：
// 上传持有组件共享锁 + 制品独占锁，只有同一制品身份才串行。
// 若退化为组件独占锁，本测试中的慢上传会阻塞快上传而超时失败。
func TestSaveDifferentArtifactsParallel(t *testing.T) {
	db := openArtifactDB(t)
	ctx := context.Background()
	sfx := time.Now().UnixNano()
	if _, err := db.Exec(`INSERT INTO tenants (id,name) VALUES (901,$1)
		ON CONFLICT (id) DO NOTHING`, fmt.Sprintf("artifact_tenant_901_%d", sfx)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM tenants WHERE id=901`) })

	var compID int64
	if err := db.QueryRow(`INSERT INTO components (name, deploy_mode, tenant_id)
		VALUES ($1,'replace',901) RETURNING id`, fmt.Sprintf("art_parallel_%d", sfx)).Scan(&compID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM components WHERE id=$1`, compID) })

	dir := t.TempDir()
	repo := NewRepo(db, dir)
	repo2 := NewRepo(db, dir)
	sc := scope.New(901, false)

	slow := &blockingReader{
		started: make(chan struct{}), release: make(chan struct{}),
		data: []byte("slow-artifact-content"),
	}
	slowDone := make(chan error, 1)
	go func() {
		_, err := repo.Save(ctx, sc, &Artifact{
			ComponentID: compID, Version: fmt.Sprintf("v-slow-%d", sfx),
			Arch: "amd64", Filename: "slow.bin",
		}, slow)
		slowDone <- err
	}()

	select {
	case <-slow.started:
	case <-time.After(5 * time.Second):
		t.Fatal("慢上传未在预期时间内进入写入阶段")
	}

	fastDone := make(chan error, 1)
	go func() {
		_, err := repo2.Save(ctx, sc, &Artifact{
			ComponentID: compID, Version: fmt.Sprintf("v-fast-%d", sfx),
			Arch: "amd64", Filename: "fast.bin",
		}, bytes.NewReader([]byte("fast-artifact-content")))
		fastDone <- err
	}()

	select {
	case err := <-fastDone:
		if err != nil {
			close(slow.release)
			<-slowDone
			t.Fatalf("不同制品并行上传失败: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(slow.release)
		<-slowDone
		t.Fatal("不同制品上传被串行化：慢上传阻塞了快上传")
	}

	close(slow.release)
	if err := <-slowDone; err != nil {
		t.Fatalf("慢上传失败: %v", err)
	}
}

// TestStorageKeyUniquePerIdentity 不同制品身份必须落到不同存储路径；
// 否则并行上传时同版本/架构、不同 OS 的同名文件会互相覆盖。
func TestStorageKeyUniquePerIdentity(t *testing.T) {
	linux := storageKey(1, "v1", "linux", "amd64", "bin")
	windows := storageKey(1, "v1", "windows", "amd64", "bin")
	generic := storageKey(1, "v1", "", "amd64", "bin")
	archOther := storageKey(1, "v1", "linux", "arm64", "bin")
	versionOther := storageKey(1, "v2", "linux", "amd64", "bin")

	seen := map[string]string{}
	for name, key := range map[string]string{
		"linux": linux, "windows": windows, "generic": generic,
		"archOther": archOther, "versionOther": versionOther,
	} {
		if prev, ok := seen[key]; ok {
			t.Fatalf("%s 与 %s 存储路径冲突: %s", prev, name, key)
		}
		seen[key] = name
	}
}
