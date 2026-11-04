// Package agentlog 提供 Agent 跨平台文件日志与自动切割。
//
// Linux/Windows 都写入固定文件，便于 `upgrade-agent logs` 统一查看；
// 切割采用 copytruncate（先复制备份再截断当前文件），避免 Windows 上
// 日志查看进程占用文件导致 rename 失败。
package agentlog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const (
	// DefaultMaxBytes 单个日志文件默认上限（10 MB）。
	DefaultMaxBytes int64 = 10 << 20
	// DefaultBackups 默认保留的历史日志份数。
	DefaultBackups = 5
)

// Writer 带大小切割的日志写入器（并发安全）。
type Writer struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	size     int64
	maxBytes int64
	backups  int
	// lastRotateErr 记录最近一次切割失败原因，便于诊断/测试（不影响正常写入）。
	lastRotateErr error
}

// Open 打开日志文件并返回切割写入器。
//
// maxBytes<=0 时使用 DefaultMaxBytes；backups<1 时使用 DefaultBackups。
func Open(path string, maxBytes int64, backups int) (*Writer, error) {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return nil, fmt.Errorf("日志路径为空")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if backups < 1 {
		backups = DefaultBackups
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}
	// 不使用 O_APPEND：Windows 上 O_APPEND 会移除 GENERIC_WRITE，
	// 导致 Truncate（日志切割）失败。写入由 Writer.mu 串行化并维护文件末尾偏移。
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0640)
	if err != nil {
		return nil, fmt.Errorf("打开日志文件失败: %w", err)
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Writer{path: path, file: f, size: fi.Size(), maxBytes: maxBytes, backups: backups}, nil
}

// Path 返回日志文件路径。
func (w *Writer) Path() string { return w.path }

// LastRotateError 返回最近一次切割失败原因（正常时为 nil）。
func (w *Writer) LastRotateError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastRotateErr
}

// Write 写入日志；超过上限时先切割。切割失败不阻塞本次写入。
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			w.lastRotateErr = err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close 关闭当前日志文件（可重复调用）。
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// rotateLocked 执行 copytruncate 切割，调用方必须持有 mu。
func (w *Writer) rotateLocked() error {
	if w.size == 0 {
		return nil
	}
	_ = w.file.Sync()
	// 旧备份依次后移：.N-1 -> .N，...，.1 -> .2。
	for i := w.backups - 1; i >= 1; i-- {
		oldPath := w.path + "." + strconv.Itoa(i)
		newPath := w.path + "." + strconv.Itoa(i+1)
		_ = os.Remove(newPath)
		if err := os.Rename(oldPath, newPath); err != nil && !os.IsNotExist(err) {
			// 备份移动失败不终止轮转，继续尝试复制当前文件。
			continue
		}
	}
	// 复制当前文件到 .1，再截断当前文件。
	// 用独立读句柄 + 显式缓冲区，避免 Windows 上 ReadFrom/copy_file_range
	// 对可写句柄或共享模式不兼容导致轮转失败。
	src, err := os.Open(w.path)
	if err != nil {
		return err
	}
	tmp := w.path + ".1.tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		src.Close()
		return err
	}
	buf := make([]byte, 64*1024)
	_, copyErr := io.CopyBuffer(dst, src, buf)
	_ = src.Close()
	if copyErr == nil {
		copyErr = dst.Sync()
	}
	closeErr := dst.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if err := os.Rename(tmp, w.path+".1"); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := w.file.Truncate(0); err != nil {
		return err
	}
	if _, err := w.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	w.size = 0
	return nil
}

// Setup 初始化全局标准库日志：同时写文件与 stderr（systemd/journal 仍可看到）。
//
// 文件写在 MultiWriter 第一位：Windows 服务模式下 stderr 可能不可用，
// 即使 stderr 写入失败也不影响文件日志落盘。
func Setup(path string, maxBytes int64, backups int) error {
	w, err := Open(path, maxBytes, backups)
	if err != nil {
		return err
	}
	currentMu.Lock()
	prev := current
	current = w
	currentMu.Unlock()
	if prev != nil {
		_ = prev.Close()
	}
	log.SetOutput(io.MultiWriter(w, os.Stderr))
	log.SetFlags(log.LstdFlags)
	return nil
}

var (
	currentMu sync.Mutex
	current   *Writer
)

// Close 关闭当前全局日志文件并恢复 stderr 输出。
func Close() error {
	currentMu.Lock()
	w := current
	current = nil
	currentMu.Unlock()
	log.SetOutput(os.Stderr)
	if w != nil {
		return w.Close()
	}
	return nil
}
