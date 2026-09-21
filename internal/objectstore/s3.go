package objectstore

// s3.go — aws-sdk-go-v2 S3 subset: static credentials, explicit endpoint,
// optional path-style. Only the two calls the offload pipeline makes
// (PutObject, HeadObject); everything else the SDK can do stays unused —
// no config-chain resolution, no session caching, no extra service clients.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/smithy-go"
)

// S3Store is the production Store: one S3-compatible bucket.
type S3Store struct {
	client          *s3.Client
	bucket          string
	accessKeyID     string
	secretAccessKey string
}

// NewS3 builds an S3Store. Credentials support ${VAR} env refs (expanded
// here, at construction — the raw config keeps the reference so a settings
// save never writes the plaintext secret back to disk). Region defaults to
// "auto" (R2/B2/MinIO ignore it; AWS users set a real one).
func NewS3(cfg Config) (*S3Store, error) {
	if strings.TrimSpace(cfg.EndpointURL) == "" {
		return nil, fmt.Errorf("objectstore: endpoint_url is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("objectstore: bucket is required")
	}
	accessKey := config.ExpandEnvRefs(cfg.AccessKeyID)
	secretKey := config.ExpandEnvRefs(cfg.SecretAccessKey)
	if strings.TrimSpace(accessKey) == "" || strings.TrimSpace(secretKey) == "" {
		return nil, fmt.Errorf("objectstore: credential is required (check access_key_id / secret_access_key, including ${VAR} expansion)")
	}
	region := cfg.Region
	if region == "" {
		region = "auto"
	}

	awsCfg := aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		// Standard SDK backoff (3 attempts) absorbs transient 5xx/connection
		// resets; sustained pacing is the iobudget tenant's job, not the
		// client's.
		RetryMaxAttempts: 3,
		EndpointResolverWithOptions: aws.EndpointResolverWithOptionsFunc(
			func(service, region string, _ ...any) (aws.Endpoint, error) {
				return aws.Endpoint{URL: cfg.EndpointURL, SigningRegion: region}, nil
			}),
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.PathStyle
	})
	return &S3Store{client: client, bucket: cfg.Bucket, accessKeyID: accessKey, secretAccessKey: secretKey}, nil
}

// Put implements Store.
func (s *S3Store) Put(ctx context.Context, key string, body io.Reader, size int64) (string, error) {
	out, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
	})
	if err != nil {
		return "", fmt.Errorf("put %s: %w", key, err)
	}
	if out.ETag != nil {
		return *out.ETag, nil
	}
	return "", nil
}

// Head implements Store.
func (s *S3Store) Head(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var respErr *awshttp.ResponseError
		if errors.As(err, &respErr) && (respErr.HTTPStatusCode() == 404 || respErr.HTTPStatusCode() == 403) {
			return ObjectInfo{}, fmt.Errorf("head %s: %w", key, ErrObjectNotFound)
		}
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && strings.Contains(strings.ToLower(apiErr.ErrorCode()), "notfound") {
			return ObjectInfo{}, fmt.Errorf("head %s: %w", key, ErrObjectNotFound)
		}
		return ObjectInfo{}, fmt.Errorf("head %s: %w", key, err)
	}
	info := ObjectInfo{Key: key}
	if out.ContentLength != nil {
		info.Size = *out.ContentLength
	}
	if out.ETag != nil {
		info.ETag = *out.ETag
	}
	if out.LastModified != nil {
		info.LastModified = *out.LastModified
	}
	return info, nil
}
