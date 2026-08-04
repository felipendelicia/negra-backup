package storage

import (
	"context"
	"fmt"
	"io"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/felipendelicia/nat-backup/internal/models"
)

// S3Backend uploads files to an S3-compatible object store.
type S3Backend struct {
	client *s3.Client
	cfg    models.S3StorageConfig
}

func NewS3Backend(cfg models.S3StorageConfig) (*S3Backend, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("s3 config: %w", err)
	}

	var opts []func(*s3.Options)
	if cfg.Endpoint != "" {
		endpoint := cfg.Endpoint
		opts = append(opts, func(o *s3.Options) {
			o.EndpointResolver = s3.EndpointResolverFromURL(endpoint)
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, opts...)
	return &S3Backend{client: client, cfg: cfg}, nil
}

func (b *S3Backend) key(filename string) string {
	prefix := b.cfg.Prefix
	if prefix == "" {
		prefix = "nat-backup"
	}
	return path.Join(prefix, filename)
}

func (b *S3Backend) Download(filename string) (io.ReadCloser, error) {
	key := b.key(filename)

	out, err := b.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(b.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get object %s: %w", key, err)
	}
	return out.Body, nil
}

func (b *S3Backend) Delete(filename string) error {
	_, err := b.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: aws.String(b.cfg.Bucket),
		Key:    aws.String(b.key(filename)),
	})
	if err != nil {
		return fmt.Errorf("s3 delete object: %w", err)
	}
	return nil
}

// The SDK marks feature/s3/manager as superseded by feature/s3/transfermanager, but
// that package is still v0.x. Moving a backup system's upload path onto an unstable
// API is a worse trade than staying on a deprecated but stable v1 one; revisit when
// transfermanager reaches v1.

// s3PartSize is the multipart chunk size. S3 allows at most 10,000 parts, so 64 MiB
// puts the ceiling at ~640 GiB per archive while keeping the memory held per
// concurrent part reasonable.
const s3PartSize = 64 * 1024 * 1024

// s3Concurrency is how many parts upload at once. Each in-flight part holds
// s3PartSize in memory, so this caps the uploader at ~192 MiB.
const s3Concurrency = 3

func (b *S3Backend) Upload(filename string, r io.Reader, size int64) error {
	key := b.key(filename)

	// A single PutObject caps out at 5 GiB, which silently made S3 unusable for any
	// backup bigger than that — the exact case this product exists for. The manager
	// splits the stream into parts, uploads them concurrently, retries individual
	// parts, and aborts the multipart upload on failure so no partial object is left
	// behind and billed. It also takes an io.Reader without a known length, so the
	// size argument is advisory here.
	uploader := manager.NewUploader(b.client, func(u *manager.Uploader) {
		u.PartSize = s3PartSize
		u.Concurrency = s3Concurrency
	})

	if _, err := uploader.Upload(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(b.cfg.Bucket),
		Key:    aws.String(key),
		Body:   r,
	}); err != nil {
		return fmt.Errorf("s3 upload %s: %w", key, err)
	}

	return nil
}
