// Package artifact 制品的存储、校验与元数据管理。
package artifact

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/keyedlock"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/pglock"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/safepath"
	"github.com/iflyelf/upgrade_mgr/internal/pkg/scope"
	"github.com/iflyelf/upgrade_mgr/internal/storage"
)

// Artifact 制品元数据。
type Artifact struct {
	ID          int64  `json:"id"`
	TenantID    int64  `json:"tenant_id"`
	ComponentID int64  `json:"component_id"`
	Version     string `json:"version"`
	OSFamily    string `json:"os_family"`
	Arch        string `json:"arch"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	MD5         string `json:"md5"`
	SHA256      string `json:"sha256"`
	Storage     string `json:"storage"`
	Path        string `json:"path"`
	InnerPath   string `json:"inner_path"`
	Notes       string `json:"notes"`
	Status      string `json:"status"`
}

// Repo 制品仓储。
//
// 存储后端由 storage.Backend 抽象（local/s3/oss），使 StorageType 配置真正生效；
// dir 仅在 local 后端下用于路径边界校验。
type Repo struct {
	db      *sql.DB
	dir     string
	backend storage.Backend
	kind    string
	// saveLocks 是同实例内的快速互斥（按制品身份），减少同一进程内的
	// 重复数据库锁等待；跨副本/多实例的一致性由 PostgreSQL advisory lock 保证。
	saveLocks *keyedlock.Locker
	// uploadLimiter 限制本实例同时处理的上传数量，支持运行期动态调整。
	uploadLimiter *uploadLimiter
	// lockWaitNs 等待跨副本 advisory lock 的最长时间（纳秒，原子读写）。
	lockWaitNs atomic.Int64
}

// 制品上传并发与锁等待默认值；可通过 artifact.max_concurrent_uploads /
// artifact.lock_wait 配置（页面保存后热更新，无需重启）。
const (
	defaultArtifactUploadConcurrency = 4
	maxArtifactUploadConcurrency     = 64
	defaultArtifactLockWait          = 30 * time.Minute
	maxArtifactLockWait              = 24 * time.Hour
)

func newRepo(db *sql.DB, dir string, backend storage.Backend, kind string) *Repo {
	r := &Repo{
		db: db, dir: dir, backend: backend, kind: kind,
		saveLocks:     keyedlock.New(),
		uploadLimiter: newUploadLimiter(defaultArtifactUploadConcurrency),
	}
	r.lockWaitNs.Store(int64(defaultArtifactLockWait))
	return r
}

// NewRepo 创建仓储；dir 为本地存储根目录。
func NewRepo(db *sql.DB, dir string) *Repo {
	b, err := storage.NewBackend(storage.Config{Type: "local", Local: storage.LocalConfig{BaseDir: dir}})
	if err != nil {
		// 目录不可用属于部署问题，交由上层启动校验；此处保留 nil 后端，Save 时报错。
		return newRepo(db, dir, nil, "local")
	}
	return newRepo(db, dir, b, "local")
}

// NewRepoWithBackend 使用指定存储后端创建仓储（支持 local/s3/oss）。
func NewRepoWithBackend(db *sql.DB, dir string, b storage.Backend, kind string) *Repo {
	if kind == "" {
		kind = "local"
	}
	return newRepo(db, dir, b, kind)
}

// SetUploadConcurrency 动态调整本实例的上传并发上限。
//
// 该值同时参与数据库连接池下限计算：每个并发上传可能持有一条 advisory
// lock 连接，连接池需要至少“并发上传数 + 常规查询余量”。
// 可在设置页保存后在线调用，不需要重启。
func (r *Repo) SetUploadConcurrency(n int) {
	if r == nil {
		return
	}
	if r.uploadLimiter == nil {
		r.uploadLimiter = newUploadLimiter(n)
		return
	}
	r.uploadLimiter.setLimit(n)
}

// AcquireUploadSlot 获取一个上传槽位；context 取消时返回错误。
//
// 返回的释放函数可重复调用，供 handler defer 使用。
func (r *Repo) AcquireUploadSlot(ctx context.Context) (func(), error) {
	if r == nil || r.uploadLimiter == nil {
		return func() {}, nil
	}
	return r.uploadLimiter.acquire(ctx)
}

// SetLockWait 动态设置等待跨副本 advisory lock 的最长时间。
//
// 使用原子读写，设置页保存后可立即影响后续上传/删除请求。
func (r *Repo) SetLockWait(d time.Duration) {
	if r == nil {
		return
	}
	if d <= 0 {
		d = defaultArtifactLockWait
	}
	if d > maxArtifactLockWait {
		d = maxArtifactLockWait
	}
	r.lockWaitNs.Store(int64(d))
}

// LockWaitDuration 返回当前生效的跨副本锁等待时长（供测试/状态展示）。
func (r *Repo) LockWaitDuration() time.Duration {
	return r.lockWaitDuration()
}

func (r *Repo) lockWaitDuration() time.Duration {
	if r == nil {
		return defaultArtifactLockWait
	}
	ns := r.lockWaitNs.Load()
	if ns <= 0 {
		return defaultArtifactLockWait
	}
	d := time.Duration(ns)
	if d > maxArtifactLockWait {
		d = maxArtifactLockWait
	}
	return d
}

// artifactComponentLockKey 组件级制品锁键。
//
// 上传持有该键的共享锁，PurgeByComponent/组件删除持有独占锁：
// 不同制品可并行上传，删除会等待所有上传完成并阻止新上传。
func artifactComponentLockKey(componentID int64) string {
	return fmt.Sprintf("artifact-component:%d", componentID)
}

// artifactIdentityLockKey 单个制品身份锁键（component/version/arch/os）。
//
// 同一身份的上传串行，不同身份并行；文件名不参与键，避免换文件名绕过。
func artifactIdentityLockKey(componentID int64, version, arch, osFamily string) string {
	return fmt.Sprintf("artifact-identity:%d|%s|%s|%s", componentID, version, arch, osFamily)
}

// LockComponent 获取组件级独占锁（删除/Purge 使用），调用方负责 Unlock。
func (r *Repo) LockComponent(ctx context.Context, componentID int64) (*pglock.MultiLock, error) {
	if componentID <= 0 {
		return nil, fmt.Errorf("组件 ID 非法")
	}
	return acquireArtifactDBLocks(ctx, r.db,
		[]pglock.LockSpec{{Key: pglock.KeyFromString(artifactComponentLockKey(componentID))}},
		r.lockWaitDuration())
}

// storageKey 构造制品在后端中的键：
// <component_id>/<version>-<os_family>-<arch>/<filename>。
//
// os_family 必须参与目录：否则同版本/架构下 Linux 与 Windows 的同名文件
// 会写到同一路径；制品改为并行上传后，两个身份会互相覆盖，导致其中一个
// 数据库行的 MD5/SHA256 与实际内容不一致。
// 各段均已通过 safepath 白名单校验，故键中不含 ../ 或分隔符注入。
func storageKey(componentID int64, version, osFamily, arch, filename string) string {
	return fmt.Sprintf("%d/%s-%s-%s/%s", componentID, version, osFamily, arch, filename)
}

// acquireArtifactDBLock 获取单个跨副本制品 advisory lock（测试/单锁场景）。
func acquireArtifactDBLock(ctx context.Context, db *sql.DB, key int64, wait time.Duration) (*pglock.MultiLock, error) {
	return acquireArtifactDBLocks(ctx, db, []pglock.LockSpec{{Key: key}}, wait)
}

// acquireArtifactDBLocks 通过轮询获取一组 advisory lock。
//
// 用 pg_try_advisory_lock(_shared) 轮询而不是阻塞式 pg_advisory_lock：
// 抢不到锁时立即释放已拿到的锁并关闭连接，不占用连接池中的连接。
// 抢到锁后返回的专用连接会一直持有到 Unlock；Save 在这条连接上写元数据，
// 不再额外从池中借连接，因此不会出现「锁连接占满池子」的死锁。
func acquireArtifactDBLocks(ctx context.Context, db *sql.DB, specs []pglock.LockSpec, wait time.Duration) (*pglock.MultiLock, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	if wait <= 0 {
		wait = defaultArtifactLockWait
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		lock, ok, err := pglock.TryMany(waitCtx, db, specs)
		if err != nil {
			return nil, fmt.Errorf("获取制品锁失败: %w", err)
		}
		if ok {
			return lock, nil
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("等待其他实例上传/删除同一组件制品超时: %w", waitCtx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Save 存储上传的制品文件并计算校验和，写入元数据。
//
// arch 必填，用于 Agent 匹配；ELF 架构与静态性校验在 checkELF 中进行（Linux ELF）。
// HTTP 上传路径会先调用 AcquireUploadSlot；连接池容量假设（上传数 + 余量）
// 依赖该槽位限制，直接调用 Save 的批量任务需自行控制并发。
func (r *Repo) Save(ctx context.Context, sc scope.Scope, meta *Artifact, src io.Reader) (*Artifact, error) {
	if meta.ComponentID == 0 || meta.Version == "" || meta.Arch == "" {
		return nil, fmt.Errorf("component_id/version/arch 必填")
	}
	// 租户隔离：制品必须挂在当前可见的组件下，防止租户管理员把制品
	// 挂到其他租户的组件 ID（制造跨租户数据/级联删除）。
	var componentTenant int64
	err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(tenant_id,1) FROM components WHERE id=$1`, meta.ComponentID).
		Scan(&componentTenant)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("组件不存在")
	}
	if err != nil {
		return nil, err
	}
	if !sc.Allows(componentTenant) {
		return nil, fmt.Errorf("组件不存在")
	}
	// 安全：清洗一切进入文件路径的用户可控字段，防止路径遍历（../ 或分隔符）。
	if err := safepath.SafeSegment(meta.Version); err != nil {
		return nil, fmt.Errorf("version 非法: %w", err)
	}
	if err := safepath.SafeSegment(meta.Arch); err != nil {
		return nil, fmt.Errorf("arch 非法: %w", err)
	}
	if meta.OSFamily != "" {
		if err := safepath.SafeSegment(meta.OSFamily); err != nil {
			return nil, fmt.Errorf("os_family 非法: %w", err)
		}
	}
	if meta.Filename == "" {
		meta.Filename = fmt.Sprintf("%s-%s-%s", meta.Version, meta.OSFamily, meta.Arch)
	}
	// 文件名白名单清洗，杜绝 ../ 与分隔符注入。
	meta.Filename = safepath.SanitizeFilename(meta.Filename)
	if r.backend == nil {
		return nil, fmt.Errorf("存储后端未初始化")
	}
	key := storageKey(meta.ComponentID, meta.Version, meta.OSFamily, meta.Arch, meta.Filename)
	// 锁模型：
	//   - 组件共享锁：允许多个不同制品并行上传；删除/Purge 的组件独占锁
	//     会等待所有上传完成并阻止新上传。
	//   - 制品独占锁：同一 component/version/arch/os 的上传仍串行，避免
	//     文件与 MD5/SHA256 元数据错配。
	// 先拿进程内锁避免本实例内的无效数据库轮询，再拿跨副本 advisory lock。
	lockKey := artifactIdentityLockKey(meta.ComponentID, meta.Version, meta.Arch, meta.OSFamily)
	if r.saveLocks != nil {
		unlock := r.saveLocks.Lock(lockKey)
		defer unlock()
	}
	specs := []pglock.LockSpec{
		{Key: pglock.KeyFromString(artifactComponentLockKey(meta.ComponentID)), Shared: true},
		{Key: pglock.KeyFromString(lockKey)},
	}
	dbLock, err := acquireArtifactDBLocks(ctx, r.db, specs, r.lockWaitDuration())
	if err != nil {
		return nil, err
	}
	defer dbLock.Unlock()
	dbConn := dbLock.Conn()
	if dbConn == nil {
		return nil, fmt.Errorf("制品上传锁连接不可用")
	}

	// 先落临时文件：便于边写边算校验和，并支持 ELF 检查（远端后端也需本地文件）。
	tmpf, err := os.CreateTemp("", "upgrade-artifact-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmpf.Name()
	defer func() {
		tmpf.Close()
		os.Remove(tmpPath)
	}()

	md5h := md5.New()
	sha := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmpf, md5h, sha), src)
	if err != nil {
		return nil, fmt.Errorf("写入制品失败: %w", err)
	}
	if err := tmpf.Sync(); err != nil {
		return nil, err
	}

	meta.Size = size
	meta.MD5 = hex.EncodeToString(md5h.Sum(nil))
	meta.SHA256 = hex.EncodeToString(sha.Sum(nil))
	meta.Storage = r.kind
	if meta.Status == "" {
		meta.Status = "active"
	}
	// 制品归属组件所在租户：superadmin 跨租户上传时也必须落在组件的租户，
	// 否则任务（按组件租户作用域查找制品）会找不到刚上传的文件。
	meta.TenantID = componentTenant

	// ELF 架构校验（非致命：非 ELF 或不匹配仅告警记入 notes）
	if warn := checkELF(tmpPath, meta.Arch); warn != "" {
		if meta.Notes != "" {
			meta.Notes += "; "
		}
		meta.Notes += warn
	}

	// 上传到存储后端（local 为落盘，s3/oss 为对象上传）。
	if _, err := tmpf.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := r.backend.Save(key, tmpf, size); err != nil {
		return nil, fmt.Errorf("存储制品失败: %w", err)
	}
	// path 统一存储后端键（local 后端可由 baseDir 还原绝对路径）。
	meta.Path = key

	err = dbConn.QueryRowContext(ctx, `
		INSERT INTO artifacts (component_id, version, os_family, arch, filename, size, md5, sha256,
			storage, path, inner_path, notes, status, tenant_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (component_id, version, arch, os_family) DO UPDATE SET
			filename=EXCLUDED.filename, size=EXCLUDED.size, md5=EXCLUDED.md5, sha256=EXCLUDED.sha256,
			path=EXCLUDED.path, inner_path=EXCLUDED.inner_path, notes=EXCLUDED.notes, status=EXCLUDED.status,
			tenant_id=EXCLUDED.tenant_id
		RETURNING id`,
		meta.ComponentID, meta.Version, meta.OSFamily, meta.Arch, meta.Filename, meta.Size,
		meta.MD5, meta.SHA256, meta.Storage, meta.Path, meta.InnerPath, meta.Notes, meta.Status,
		meta.TenantID,
	).Scan(&meta.ID)
	if err != nil {
		// 元数据写入失败则回滚已上传对象，避免留下孤儿文件。
		_ = r.backend.Delete(key)
		return nil, err
	}
	return meta, nil
}

// List 列出制品（可按 component_id 过滤，0 表示全部）。
func (r *Repo) List(ctx context.Context, sc scope.Scope, componentID int64) ([]*Artifact, error) {
	q := `SELECT id, tenant_id, component_id, version, os_family, arch, filename, size, md5, sha256,
		storage, path, inner_path, notes, status FROM artifacts`
	cond, targs := sc.Where("", 1)
	args := append([]any{}, targs...)
	q += ` WHERE ` + cond
	if componentID > 0 {
		q += fmt.Sprintf(` AND component_id=$%d`, len(args)+1)
		args = append(args, componentID)
	}
	q += ` ORDER BY component_id, version DESC`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.TenantID, &a.ComponentID, &a.Version, &a.OSFamily, &a.Arch, &a.Filename,
			&a.Size, &a.MD5, &a.SHA256, &a.Storage, &a.Path, &a.InnerPath, &a.Notes, &a.Status); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// Get 按 ID 获取。
func (r *Repo) Get(ctx context.Context, sc scope.Scope, id int64) (*Artifact, error) {
	var a Artifact
	cond, targs := sc.Where("", 2)
	err := r.db.QueryRowContext(ctx, `SELECT id, tenant_id, component_id, version, os_family, arch, filename,
		size, md5, sha256, storage, path, inner_path, notes, status FROM artifacts WHERE id=$1 AND `+cond,
		append([]any{id}, targs...)...).
		Scan(&a.ID, &a.TenantID, &a.ComponentID, &a.Version, &a.OSFamily, &a.Arch, &a.Filename,
			&a.Size, &a.MD5, &a.SHA256, &a.Storage, &a.Path, &a.InnerPath, &a.Notes, &a.Status)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("制品不存在")
	}
	return &a, err
}

// Find 按 组件+版本+arch 查找匹配制品（os_family 空表示通用）。
// arch=all 表示与架构无关的制品（如 Shell 安装脚本），可匹配任意主机；
// 同时存在精确架构和 all 时优先精确架构。
func (r *Repo) Find(ctx context.Context, sc scope.Scope, componentID int64, version, arch, osFamily string) (*Artifact, error) {
	var a Artifact
	cond, targs := sc.Where("", 5)
	err := r.db.QueryRowContext(ctx, `SELECT id, tenant_id, component_id, version, os_family, arch, filename,
		size, md5, sha256, storage, path, inner_path, notes, status FROM artifacts
		WHERE component_id=$1 AND version=$2 AND (arch=$3 OR arch='all') AND (os_family=$4 OR os_family='') AND `+cond+`
		ORDER BY CASE WHEN arch=$3 THEN 0 ELSE 1 END,
		         CASE WHEN os_family=$4 THEN 0 ELSE 1 END LIMIT 1`,
		append([]any{componentID, version, arch, osFamily}, targs...)...).
		Scan(&a.ID, &a.TenantID, &a.ComponentID, &a.Version, &a.OSFamily, &a.Arch, &a.Filename,
			&a.Size, &a.MD5, &a.SHA256, &a.Storage, &a.Path, &a.InnerPath, &a.Notes, &a.Status)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("未找到匹配制品(组件=%d 版本=%s arch=%s 或 all)", componentID, version, arch)
	}
	return &a, err
}

// PurgeByComponent 删除组件下全部制品的磁盘文件与元数据。
//
// 使用与上传相同的组件级 advisory lock，删除期间不会插入新制品；
// 组件删除 handler 也复用该锁，避免“删除后又被上传写回”。
//
// 安全要点：path 取自数据库，若库被篡改可能指向任意路径，
// 因此删除前强制校验其位于制品根目录内（EnsureWithin），越界则跳过并告警。
func (r *Repo) PurgeByComponent(ctx context.Context, sc scope.Scope, componentID int64) error {
	// 与上传共用组件级锁：确保删除期间不会插入新制品，也不会删除正在写入的文件。
	lock, err := r.LockComponent(ctx, componentID)
	if err != nil {
		return err
	}
	defer lock.Unlock()

	paths, err := r.PathsByComponent(ctx, sc, componentID)
	if err != nil {
		return err
	}
	r.RemoveObjects(paths)
	// 元数据由 components 的 ON DELETE CASCADE 清理；此处显式删除以支持独立调用。
	dcond, dargs := sc.Where("", 2)
	_, err = r.db.ExecContext(ctx, `DELETE FROM artifacts WHERE component_id=$1 AND `+dcond,
		append([]any{componentID}, dargs...)...)
	return err
}

// PathsByComponent 返回组件下全部制品的存储路径（只读，不删除）。
//
// 删除组件时应先取路径、再删组件（元数据随外键 CASCADE 消失）、最后删文件；
// 这样组件删除失败时不会出现“文件已删但组件还在”的数据丢失。
func (r *Repo) PathsByComponent(ctx context.Context, sc scope.Scope, componentID int64) ([]string, error) {
	cond, targs := sc.Where("", 2)
	rows, err := r.db.QueryContext(ctx,
		`SELECT path FROM artifacts WHERE component_id=$1 AND `+cond,
		append([]any{componentID}, targs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil && p != "" {
			paths = append(paths, p)
		}
	}
	return paths, rows.Err()
}

// RemoveObjects 尽力删除给定存储路径对应的对象；失败只告警，不阻断主流程。
func (r *Repo) RemoveObjects(paths []string) {
	for _, p := range paths {
		// 安全：path 取自数据库，若被篡改可能指向任意位置；
		// deleteObject 内部对 local 后端强制边界校验，越界直接报错跳过。
		if err := r.deleteObject(p); err != nil {
			log.Printf("⚠️ 删除制品失败（已跳过）%s: %v", p, err)
		}
	}
}

// resolveLocalPath 将存储键还原为本地绝对路径（仅 local 后端）。
//
// 兼容历史数据：早期版本 path 存的是绝对路径，此处原样返回并做边界校验。
func (r *Repo) resolveLocalPath(p string) (string, error) {
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(r.dir, p)
	}
	if err := safepath.EnsureWithin(r.dir, abs); err != nil {
		return "", err
	}
	return abs, nil
}

// Open 打开制品内容，返回可 Seek 的读取器（支持 HTTP Range）。
//
// local 后端直接打开文件；远端后端（s3/oss）若不可 Seek 则落临时文件，
// 以保证下载端点的 Range 续传语义一致。
func (r *Repo) Open(a *Artifact) (io.ReadSeekCloser, error) {
	if r.kind == "local" || a.Storage == "local" {
		abs, err := r.resolveLocalPath(a.Path)
		if err != nil {
			return nil, err
		}
		return os.Open(abs)
	}
	if r.backend == nil {
		return nil, fmt.Errorf("存储后端未初始化")
	}
	rc, err := r.backend.Get(a.Path)
	if err != nil {
		return nil, err
	}
	if rs, ok := rc.(io.ReadSeekCloser); ok {
		return rs, nil
	}
	defer rc.Close()
	tmp, err := os.CreateTemp("", "upgrade-dl-*")
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(tmp, rc); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	// 关闭即删除：包一层，避免临时文件泄漏。
	return &tempReadSeekCloser{File: tmp}, nil
}

// tempReadSeekCloser 关闭时同时删除临时文件。
type tempReadSeekCloser struct{ *os.File }

func (t *tempReadSeekCloser) Close() error {
	name := t.File.Name()
	err := t.File.Close()
	_ = os.Remove(name)
	return err
}

// deleteObject 删除存储后端中的对象，兼容历史绝对路径。
func (r *Repo) deleteObject(p string) error {
	if r.kind == "local" {
		abs, err := r.resolveLocalPath(p)
		if err != nil {
			return err
		}
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if r.backend == nil {
		return fmt.Errorf("存储后端未初始化")
	}
	return r.backend.Delete(p)
}

// ListPaged 分页查询制品（支持组件过滤与关键字搜索）。
//
// 与 List 并存：任务页需要完整列表做版本下拉，列表页使用分页避免大表全量传输。
func (r *Repo) ListPaged(ctx context.Context, sc scope.Scope, componentID int64, q string, page, size int) ([]*Artifact, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	cond, targs := sc.Where("", 1)
	where := []string{cond}
	args := append([]any{}, targs...)
	if componentID > 0 {
		args = append(args, componentID)
		where = append(where, fmt.Sprintf("component_id=$%d", len(args)))
	}
	if q = strings.TrimSpace(q); q != "" {
		args = append(args, "%"+q+"%")
		n := len(args)
		where = append(where, fmt.Sprintf(
			"(version ILIKE $%d OR filename ILIKE $%d OR notes ILIKE $%d OR inner_path ILIKE $%d)",
			n, n, n, n))
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM artifacts WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	listArgs := append(append([]any{}, args...), size, (page-1)*size)
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, tenant_id, component_id, version, os_family, arch, filename, size, md5, sha256,
		       storage, path, inner_path, notes, status
		  FROM artifacts WHERE %s
		 ORDER BY id DESC LIMIT $%d OFFSET $%d`,
		whereSQL, len(listArgs)-1, len(listArgs)), listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]*Artifact, 0, size)
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.TenantID, &a.ComponentID, &a.Version, &a.OSFamily, &a.Arch, &a.Filename,
			&a.Size, &a.MD5, &a.SHA256, &a.Storage, &a.Path, &a.InnerPath, &a.Notes, &a.Status); err != nil {
			return nil, 0, err
		}
		out = append(out, &a)
	}
	return out, total, rows.Err()
}
