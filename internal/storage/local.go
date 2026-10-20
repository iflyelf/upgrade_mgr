package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	// 纵深防御：确保最终路径在 baseDir 内
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(l.baseDir)) {
		return "", fmt.Errorf("路径越界: %s", key)
	}
	
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	// 原子写：先写临时文件再 rename
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(f, reader); err != nil {
		os.Remove(tmp)
		return "", err
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
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
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(l.baseDir)) {
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
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(l.baseDir)) {
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
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(l.baseDir)) {
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
