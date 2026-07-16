package artifacts

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/rancher/tests/internal/agenticqa/types"
)

type s3Publisher struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	prefix  string
	ttl     time.Duration
}

func (p *s3Publisher) Publish(ctx context.Context, source, name string) (types.ArtifactRef, error) {
	name, err := safeName(name)
	if err != nil {
		return types.ArtifactRef{}, err
	}
	hash, size, err := fileMetadata(source)
	if err != nil {
		return types.ArtifactRef{}, err
	}
	f, err := os.Open(source)
	if err != nil {
		return types.ArtifactRef{}, err
	}
	defer f.Close()
	key := strings.TrimPrefix(path.Join(p.prefix, name), "/")
	if _, err := p.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(p.bucket), Key: aws.String(key), Body: f, Metadata: map[string]string{"sha256": hash}}); err != nil {
		return types.ArtifactRef{}, fmt.Errorf("uploading s3://%s/%s: %w", p.bucket, key, err)
	}
	presigned, err := p.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(p.bucket), Key: aws.String(key)}, s3.WithPresignExpires(p.ttl))
	if err != nil {
		return types.ArtifactRef{}, fmt.Errorf("presigning s3://%s/%s: %w", p.bucket, key, err)
	}
	return types.ArtifactRef{Backend: "s3", URI: "s3://" + p.bucket + "/" + key, URL: presigned.URL, Bucket: p.bucket, Key: key, SHA256: hash, Size: size}, nil
}
