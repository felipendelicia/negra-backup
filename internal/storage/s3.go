package storage

import (
	"context"
	"fmt"
	"io"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
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

func (b *S3Backend) Upload(filename string, r io.Reader, size int64) error {
	key := path.Join("nat-backup", filename)

	_, err := b.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:        aws.String(b.cfg.Bucket),
		Key:           aws.String(key),
		Body:          r,
		ContentLength: size,
	})
	if err != nil {
		return fmt.Errorf("s3 put object: %w", err)
	}

	return nil
}
