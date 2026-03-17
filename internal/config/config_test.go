package config

import (
	"os"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any env vars that might interfere.
	for _, key := range []string{
		"SUBSARR_DB_DRIVER", "SUBSARR_DB_DSN", "SUBSARR_STORAGE_BACKEND",
		"SUBSARR_STORAGE_PATH", "SUBSARR_S3_ENDPOINT", "SUBSARR_S3_BUCKET",
		"SUBSARR_S3_REGION", "SUBSARR_S3_ACCESS_KEY", "SUBSARR_S3_SECRET_KEY",
		"SUBSARR_S3_PATH_STYLE", "SUBSARR_LISTEN",
	} {
		t.Setenv(key, "")
	}

	cfg := Load()

	if cfg.DBDriver != "sqlite" {
		t.Errorf("DBDriver = %q, want %q", cfg.DBDriver, "sqlite")
	}
	if cfg.DBDSN != "subsarr.db" {
		t.Errorf("DBDSN = %q, want %q", cfg.DBDSN, "subsarr.db")
	}
	if cfg.StorageBackend != "filesystem" {
		t.Errorf("StorageBackend = %q, want %q", cfg.StorageBackend, "filesystem")
	}
	if cfg.StoragePath != "./storage" {
		t.Errorf("StoragePath = %q, want %q", cfg.StoragePath, "./storage")
	}
	if cfg.S3Bucket != "subsarr" {
		t.Errorf("S3Bucket = %q, want %q", cfg.S3Bucket, "subsarr")
	}
	if cfg.S3Region != "us-east-1" {
		t.Errorf("S3Region = %q, want %q", cfg.S3Region, "us-east-1")
	}
	if cfg.Listen != "0.0.0.0:8090" {
		t.Errorf("Listen = %q, want %q", cfg.Listen, "0.0.0.0:8090")
	}
	if cfg.S3PathStyle {
		t.Error("S3PathStyle should default to false")
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("SUBSARR_DB_DRIVER", "postgres")
	t.Setenv("SUBSARR_DB_DSN", "postgres://localhost/test")
	t.Setenv("SUBSARR_STORAGE_BACKEND", "s3")
	t.Setenv("SUBSARR_STORAGE_PATH", "/custom/path")
	t.Setenv("SUBSARR_S3_ENDPOINT", "http://s3.local:9000")
	t.Setenv("SUBSARR_S3_BUCKET", "my-bucket")
	t.Setenv("SUBSARR_S3_REGION", "eu-west-1")
	t.Setenv("SUBSARR_S3_ACCESS_KEY", "AKID")
	t.Setenv("SUBSARR_S3_SECRET_KEY", "SECRET")
	t.Setenv("SUBSARR_S3_PATH_STYLE", "true")
	t.Setenv("SUBSARR_LISTEN", "127.0.0.1:9090")

	cfg := Load()

	if cfg.DBDriver != "postgres" {
		t.Errorf("DBDriver = %q, want %q", cfg.DBDriver, "postgres")
	}
	if cfg.DBDSN != "postgres://localhost/test" {
		t.Errorf("DBDSN = %q, want %q", cfg.DBDSN, "postgres://localhost/test")
	}
	if cfg.StorageBackend != "s3" {
		t.Errorf("StorageBackend = %q, want %q", cfg.StorageBackend, "s3")
	}
	if cfg.S3Endpoint != "http://s3.local:9000" {
		t.Errorf("S3Endpoint = %q, want %q", cfg.S3Endpoint, "http://s3.local:9000")
	}
	if cfg.S3AccessKey != "AKID" {
		t.Errorf("S3AccessKey = %q, want %q", cfg.S3AccessKey, "AKID")
	}
	if cfg.S3SecretKey != "SECRET" {
		t.Errorf("S3SecretKey = %q, want %q", cfg.S3SecretKey, "SECRET")
	}
	if !cfg.S3PathStyle {
		t.Error("S3PathStyle should be true when env is 'true'")
	}
	if cfg.Listen != "127.0.0.1:9090" {
		t.Errorf("Listen = %q, want %q", cfg.Listen, "127.0.0.1:9090")
	}
}

func TestEnvOr(t *testing.T) {
	const key = "SUBSARR_TEST_ENVVAR_XYZ"
	os.Unsetenv(key)

	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("envOr unset = %q, want %q", got, "fallback")
	}

	t.Setenv(key, "override")
	if got := envOr(key, "fallback"); got != "override" {
		t.Errorf("envOr set = %q, want %q", got, "override")
	}
}
