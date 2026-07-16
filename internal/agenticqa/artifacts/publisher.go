// Package artifacts publishes environment bundles.
package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/rancher/tests/internal/agenticqa/runconfig"
	"github.com/rancher/tests/internal/agenticqa/types"
)

type Publisher interface {
	Publish(context.Context, string, string) (types.ArtifactRef, error)
}

// Delete removes a tracked artifact.
func Delete(ctx context.Context, cfg runconfig.ArtifactConfig, ref types.ArtifactRef) error {
	switch ref.Backend {
	case "local":
		if ref.LocalPath == "" {
			return nil
		}
		return os.Remove(ref.LocalPath)
	case "s3":
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
		if err != nil {
			return err
		}
		client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			o.UsePathStyle = cfg.ForcePathStyle
			if cfg.Endpoint != "" {
				o.BaseEndpoint = &cfg.Endpoint
			}
		})
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &ref.Bucket, Key: &ref.Key})
		return err
	default:
		return fmt.Errorf("unsupported artifact backend %q", ref.Backend)
	}
}

func New(ctx context.Context, cfg runconfig.ArtifactConfig, root string) (Publisher, error) {
	switch cfg.Backend {
	case "", "local":
		if root == "" {
			return nil, fmt.Errorf("local artifact root is required")
		}
		return &localPublisher{root: root}, nil
	case "s3":
		if cfg.Bucket == "" {
			return nil, fmt.Errorf("artifacts.bucket is required for s3")
		}
		ttl, err := time.ParseDuration(cfg.URLTTL)
		if err != nil || ttl <= 0 {
			return nil, fmt.Errorf("invalid artifacts.urlTTL %q", cfg.URLTTL)
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
		if err != nil {
			return nil, fmt.Errorf("loading AWS config: %w", err)
		}
		client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			o.UsePathStyle = cfg.ForcePathStyle
			if cfg.Endpoint != "" {
				o.BaseEndpoint = &cfg.Endpoint
			}
		})
		return &s3Publisher{client: client, presign: s3.NewPresignClient(client), bucket: cfg.Bucket, prefix: cfg.Prefix, ttl: ttl}, nil
	default:
		return nil, fmt.Errorf("unsupported artifact backend %q", cfg.Backend)
	}
}

func fileMetadata(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s is not a regular file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), info.Size(), nil
}

func safeName(name string) (string, error) {
	name = filepath.ToSlash(filepath.Clean(name))
	if name == "." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe artifact name %q", name)
	}
	return name, nil
}
