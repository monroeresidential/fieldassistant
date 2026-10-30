package blob

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// defaultRegion is the region whose buckets take no location constraint.
const defaultRegion = "us-east-1"

// S3Config locates an S3-compatible bucket and the key pair that may use it.
type S3Config struct {
	Endpoint, Region, Bucket, AccessKey, SecretKey string
	// PathStyle addresses the bucket as <endpoint>/<bucket> rather than
	// <bucket>.<endpoint>; MinIO needs it.
	PathStyle bool
}

// S3 is a Store backed by one bucket of an S3-compatible service.
type S3 struct {
	client *s3.Client
	bucket string
	region string
}

var _ Store = (*S3)(nil)

// NewS3 builds a client for cfg's bucket with static credentials. It makes
// no network calls.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Region == "" || cfg.Bucket == "" {
		return nil, errors.New("blob: S3 endpoint, region and bucket are required")
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.Endpoint),
		Region:       cfg.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		UsePathStyle: cfg.PathStyle,
		// Compute and check checksums only where the API requires them:
		// S3-compatible services do not all accept the SDK's default
		// trailing checksums.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &S3{client: client, bucket: cfg.Bucket, region: cfg.Region}, nil
}

// EnsureBucket creates the bucket when it does not exist yet.
func (s *S3) EnsureBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err == nil {
		return nil
	}
	var notFound *types.NotFound
	if !errors.As(err, &notFound) {
		return fmt.Errorf("blob: head bucket %s: %w", s.bucket, err)
	}
	in := &s3.CreateBucketInput{Bucket: aws.String(s.bucket)}
	if s.region != defaultRegion {
		in.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(s.region),
		}
	}
	if _, err := s.client.CreateBucket(ctx, in); err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		if errors.As(err, &owned) {
			return nil
		}
		return fmt.Errorf("blob: create bucket %s: %w", s.bucket, err)
	}
	return nil
}

// Put uploads size bytes from r. Over a plain-HTTP endpoint (local MinIO)
// the SDK signs the payload, so r must also be an io.Seeker there.
func (s *S3) Put(ctx context.Context, key, contentType string, size int64, r io.Reader) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          r,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("blob: put %s: %w", key, err)
	}
	return nil
}

// Get opens the object; a missing key is ErrNotFound.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, fmt.Errorf("blob: get %s: %w", key, err)
	}
	return out.Body, aws.ToInt64(out.ContentLength), nil
}

// Delete removes the object; a missing key is not an error.
func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil
		}
		return fmt.Errorf("blob: delete %s: %w", key, err)
	}
	return nil
}
