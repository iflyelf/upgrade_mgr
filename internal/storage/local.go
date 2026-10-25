package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iflyelf/upgrade_mgr/internal/pkg/safepath"
)

// Local 本地文件系统存储。
type Local struct {
	baseDir string
}

// NewLocal 创建本地存储后端。
func NewLocal(cfg LocalConfig) (*Local, error) {
	if cfg.BaseDir == "" {
		cfg.BaseDir = "/data/artifacts"
	}
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		return nil, fmt.Errorf("创建存储目录失败: %w", err)
	}
	return &Local{baseDir: cfg.BaseDir}, nil
}

// Save 保存制品。
func (l *Local) Save(key string, reader io.Reader, size int64) (string, error) {
	// 安全：校验 key 防止路径遍历（拒绝 ../ / \ 等）
	if err := validateKey(key); err != nil {
		return "", fmt.Errorf("非法 key: %w", err)
	}

	path := filepath.Join(l.baseDir, key)
	// 纵深防御：确保最终路径在 baseDir 内（前缀判断对 artifacts-evil 这类
	// 相邻目录会误放行，使用 filepath.Rel 做真正的边界校验）。
	if err := safepath.EnsureWithin(l.baseDir, path); err != nil {
		return "", fmt.Errorf("路径越界: %s", key)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	// 并发安全：同 key 的并发上传不能共用固定 .tmp 文件（会互相覆盖/半写）。
	// 每次使用唯一临时文件，完整写入并 close 后再原子 rename；最后一次获胜。
	tmpf, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return "", err
	}
	tmp := tmpf.Name()
	defer os.Remove(tmp)

	if _, err := io.Copy(tmpf, reader); err != nil {
		tmpf.Close()
		return "", err
	}
	if err := tmpf.Sync(); err != nil {
		tmpf.Close()
		return "", err
	}
	if err := tmpf.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}

	return key, nil
}

// Get 获取制品。
func (l *Local) Get(key string) (io.ReadCloser, error) {
	if err := validateKey(key); err != nil {
		return nil, fmt.Errorf("非法 key: %w", err)
	}
	path := filepath.Join(l.baseDir, key)
	if err := safepath.EnsureWithin(l.baseDir, path); err != nil {
		return nil, fmt.Errorf("路径越界: %s", key)
	}
	return os.Open(path)
}

// Delete 删除制品。
func (l *Local) Delete(key string) error {
	if err := validateKey(key); err != nil {
		return fmt.Errorf("非法 key: %w", err)
	}
	path := filepath.Join(l.baseDir, key)
	if err := safepath.EnsureWithin(l.baseDir, path); err != nil {
		return fmt.Errorf("路径越界: %s", key)
	}
	return os.Remove(path)
}

// Exists 检查是否存在。
func (l *Local) Exists(key string) (bool, error) {
	if err := validateKey(key); err != nil {
		return false, fmt.Errorf("非法 key: %w", err)
	}
	path := filepath.Join(l.baseDir, key)
	if err := safepath.EnsureWithin(l.baseDir, path); err != nil {
		return false, fmt.Errorf("路径越界: %s", key)
	}
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// URL 本地存储返回相对路径（由 HTTP handler 提供服务）。
func (l *Local) URL(key string, ttl int) (string, error) {
	if err := validateKey(key); err != nil {
		return "", fmt.Errorf("非法 key: %w", err)
	}
	return "/api/artifacts/download/" + key, nil
}

// validateKey 校验 key 防止路径遍历攻击。
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("key 为空")
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("key 不能包含 ..")
	}
	if strings.HasPrefix(key, "/") || strings.HasPrefix(key, "\\") {
		return fmt.Errorf("key 不能以 / 或 \\ 开头")
	}
	// 拒绝 Windows 绝对路径（C:\ D:\ 等）
	if len(key) >= 2 && key[1] == ':' {
		return fmt.Errorf("key 不能包含盘符")
	}
	return nil
}
