package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/slimcdk/subsarr/internal/config"
)

// Store abstracts subtitle file storage (local filesystem or S3-compatible).
type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// New creates a Store from the application config.
func New(cfg config.Config) (Store, error) {
	switch cfg.StorageBackend {
	case "s3":
		return NewS3(cfg.S3Endpoint, cfg.S3Bucket, cfg.S3Region, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3PathStyle)
	case "filesystem", "":
		return NewFilesystem(cfg.StoragePath), nil
	default:
		return nil, fmt.Errorf("unsupported storage backend: %s", cfg.StorageBackend)
	}
}
