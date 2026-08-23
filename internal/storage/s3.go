package storage

import (
	"context"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3 stores files in an S3-compatible object store.
type S3 struct {
	client *s3.Client
	bucket string
}

func NewS3(endpoint, bucket, region, accessKey, secretKey string, pathStyle bool) (*S3, error) {
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		UsePathStyle: pathStyle,
	})
	return &S3{client: client, bucket: bucket}, nil
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	input := &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
		Body:   r,
	}
	if size > 0 {
		input.ContentLength = &size
	}
	_, err := s.client.PutObject(ctx, input)
	return err
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, err
	}
	return output.Body, nil
}

// Exists reports whether an object is already stored. HEAD is cheap enough that
// the importer asks before every write and skips the ones already there.
func (s *S3) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err == nil {
		return true, nil
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return false, nil
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return false, nil
	}
	return false, err
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	return err
}
