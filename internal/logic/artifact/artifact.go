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
}

// NewRepo 创建仓储；dir 为本地存储根目录。
func NewRepo(db *sql.DB, dir string) *Repo {
	b, err := storage.NewBackend(storage.Config{Type: "local", Local: storage.LocalConfig{BaseDir: dir}})
	if err != nil {
		// 目录不可用属于部署问题，交由上层启动校验；此处保留 nil 后端，Save 时报错。
		return &Repo{db: db, dir: dir, kind: "local"}
	}
	return &Repo{db: db, dir: dir, backend: b, kind: "local"}
}

// NewRepoWithBackend 使用指定存储后端创建仓储（支持 local/s3/oss）。
func NewRepoWithBackend(db *sql.DB, dir string, b storage.Backend, kind string) *Repo {
	if kind == "" {
		kind = "local"
	}
	return &Repo{db: db, dir: dir, backend: b, kind: kind}
}

// storageKey 构造制品在后端中的键：<component_id>/<version>-<arch>/<filename>。
// 各段均已通过 safepath 白名单校验，故键中不含 ../ 或分隔符注入。
func storageKey(componentID int64, version, arch, filename string) string {
	return fmt.Sprintf("%d/%s-%s/%s", componentID, version, arch, filename)
}

// Save 存储上传的制品文件并计算校验和，写入元数据。
//
// arch 必填，用于 Agent 匹配；ELF 架构与静态性校验在 checkELF 中进行（Linux ELF）。
func (r *Repo) Save(ctx context.Context, sc scope.Scope, meta *Artifact, src io.Reader) (*Artifact, error) {
	if meta.ComponentID == 0 || meta.Version == "" || meta.Arch == "" {
		return nil, fmt.Errorf("component_id/version/arch 必填")
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
	meta.TenantID = sc.TenantForWrite()

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
	key := storageKey(meta.ComponentID, meta.Version, meta.Arch, meta.Filename)
	if _, err := r.backend.Save(key, tmpf, size); err != nil {
		return nil, fmt.Errorf("存储制品失败: %w", err)
	}
	// path 统一存储后端键（local 后端可由 baseDir 还原绝对路径）。
	meta.Path = key

	err = r.db.QueryRowContext(ctx, `
		INSERT INTO artifacts (component_id, version, os_family, arch, filename, size, md5, sha256,
			storage, path, inner_path, notes, status, tenant_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (component_id, version, arch, os_family) DO UPDATE SET
			filename=EXCLUDED.filename, size=EXCLUDED.size, md5=EXCLUDED.md5, sha256=EXCLUDED.sha256,
			path=EXCLUDED.path, inner_path=EXCLUDED.inner_path, notes=EXCLUDED.notes, status=EXCLUDED.status
		RETURNING id`,
		meta.ComponentID, meta.Version, meta.OSFamily, meta.Arch, meta.Filename, meta.Size,
		meta.MD5, meta.SHA256, meta.Storage, meta.Path, meta.InnerPath, meta.Notes, meta.Status,
		sc.TenantForWrite(),
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
// 安全要点：path 取自数据库，若库被篡改可能指向任意路径，
// 因此删除前强制校验其位于制品根目录内（EnsureWithin），越界则跳过并告警。
func (r *Repo) PurgeByComponent(ctx context.Context, sc scope.Scope, componentID int64) error {
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
