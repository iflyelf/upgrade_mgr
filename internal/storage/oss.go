package storage

import (
	"fmt"
	"io"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// OSS 阿里云 OSS 存储。
type OSS struct {
	client *oss.Client
	bucket *oss.Bucket
}

// NewOSS 创建 OSS 存储后端。
func NewOSS(cfg OSSConfig) (*OSS, error) {
	client, err := oss.New(cfg.Endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("创建 OSS 客户端失败: %w", err)
	}

	bucket, err := client.Bucket(cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("获取 bucket 失败: %w", err)
	}

	return &OSS{client: client, bucket: bucket}, nil
}

// Save 保存制品。
func (o *OSS) Save(key string, reader io.Reader, size int64) (string, error) {
	err := o.bucket.PutObject(key, reader)
	if err != nil {
		return "", err
	}
	return key, nil
}

// Get 获取制品。
func (o *OSS) Get(key string) (io.ReadCloser, error) {
	return o.bucket.GetObject(key)
}

// Delete 删除制品。
func (o *OSS) Delete(key string) error {
	return o.bucket.DeleteObject(key)
}

// Exists 检查是否存在。
func (o *OSS) Exists(key string) (bool, error) {
	return o.bucket.IsObjectExist(key)
}

// URL 生成预签名下载 URL。
func (o *OSS) URL(key string, ttl int) (string, error) {
	if ttl <= 0 {
		ttl = 3600
	}
	return o.bucket.SignURL(key, oss.HTTPGet, int64(ttl))
}
