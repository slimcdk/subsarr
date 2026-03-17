package config

import "os"

type Config struct {
	DBDriver       string
	DBDSN          string
	StorageBackend string
	StoragePath    string
	S3Endpoint     string
	S3Bucket       string
	S3Region       string
	S3AccessKey    string
	S3SecretKey    string
	S3PathStyle    bool
	Listen         string
}

func Load() Config {
	return Config{
		DBDriver:       envOr("SUBSARR_DB_DRIVER", "sqlite"),
		DBDSN:          envOr("SUBSARR_DB_DSN", "subsarr.db"),
		StorageBackend: envOr("SUBSARR_STORAGE_BACKEND", "filesystem"),
		StoragePath:    envOr("SUBSARR_STORAGE_PATH", "./data/storage"),
		S3Endpoint:     os.Getenv("SUBSARR_S3_ENDPOINT"),
		S3Bucket:       envOr("SUBSARR_S3_BUCKET", "subsarr"),
		S3Region:       envOr("SUBSARR_S3_REGION", "us-east-1"),
		S3AccessKey:    os.Getenv("SUBSARR_S3_ACCESS_KEY"),
		S3SecretKey:    os.Getenv("SUBSARR_S3_SECRET_KEY"),
		S3PathStyle:    os.Getenv("SUBSARR_S3_PATH_STYLE") == "true",
		Listen:         envOr("SUBSARR_LISTEN", "0.0.0.0:8090"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
