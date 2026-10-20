package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 S3 兼容存储（支持 AWS S3/MinIO/Ceph）。
type S3 struct {
	client *minio.Client
	bucket string
}

// NewS3 创建 S3 存储后端。
func NewS3(cfg S3Config) (*S3, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 S3 客户端失败: %w", err)
	}

	// 检查 bucket 是否存在
	ctx := context.Background()
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("检查 bucket 失败: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("bucket %s 不存在", cfg.Bucket)
	}

	return &S3{client: client, bucket: cfg.Bucket}, nil
}

// Save 保存制品。
func (s *S3) Save(key string, reader io.Reader, size int64) (string, error) {
	ctx := context.Background()
	_, err := s.client.PutObject(ctx, s.bucket, key, reader, size, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// Get 获取制品。
func (s *S3) Get(key string) (io.ReadCloser, error) {
	ctx := context.Background()
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

// Delete 删除制品。
func (s *S3) Delete(key string) error {
	ctx := context.Background()
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

// Exists 检查是否存在。
func (s *S3) Exists(key string) (bool, error) {
	ctx := context.Background()
	_, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// URL 生成预签名下载 URL。
func (s *S3) URL(key string, ttl int) (string, error) {
	ctx := context.Background()
	if ttl <= 0 {
		ttl = 3600
	}
	url, err := s.client.PresignedGetObject(ctx, s.bucket, key, time.Duration(ttl)*time.Second, nil)
	if err != nil {
		return "", err
	}
	return url.String(), nil
}
