// Package storage 制品存储抽象（本地/S3/OSS）。
package storage

import (
	"fmt"
	"io"
)

// Backend 存储后端接口。
type Backend interface {
	// Save 保存制品，返回存储路径。
	Save(key string, reader io.Reader, size int64) (string, error)

	// Get 获取制品读取器。
	Get(key string) (io.ReadCloser, error)

	// Delete 删除制品。
	Delete(key string) error

	// Exists 检查制品是否存在。
	Exists(key string) (bool, error)

	// URL 获取可访问 URL（支持签名 URL）。
	URL(key string, ttl int) (string, error)
}

// Config 存储配置。
type Config struct {
	Type   string            // local/s3/oss
	Local  LocalConfig       // 本地存储配置
	S3     S3Config          // S3 配置
	OSS    OSSConfig         // 阿里云 OSS 配置
	Params map[string]string // 额外参数
}

// LocalConfig 本地存储配置。
type LocalConfig struct {
	BaseDir string
}

// S3Config S3 兼容对象存储配置。
type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	UseSSL          bool
}

// OSSConfig 阿里云 OSS 配置。
type OSSConfig struct {
	Endpoint        string
	Bucket          string
	AccessKeyID     string
	AccessKeySecret string
}

// NewBackend 根据配置创建存储后端。
func NewBackend(cfg Config) (Backend, error) {
	switch cfg.Type {
	case "local", "":
		return NewLocal(cfg.Local)
	case "s3":
		return NewS3(cfg.S3)
	case "oss":
		return NewOSS(cfg.OSS)
	default:
		return nil, fmt.Errorf("不支持的存储类型: %s", cfg.Type)
	}
}
