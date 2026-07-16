package artifacts

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/rancher/tests/internal/agenticqa/types"
)

type localPublisher struct{ root string }

func (p *localPublisher) Publish(_ context.Context, source, name string) (types.ArtifactRef, error) {
	name, err := safeName(name)
	if err != nil {
		return types.ArtifactRef{}, err
	}
	dest := filepath.Join(p.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return types.ArtifactRef{}, err
	}
	in, err := os.Open(source)
	if err != nil {
		return types.ArtifactRef{}, err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return types.ArtifactRef{}, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return types.ArtifactRef{}, err
	}
	if err := out.Close(); err != nil {
		return types.ArtifactRef{}, err
	}
	hash, size, err := fileMetadata(dest)
	if err != nil {
		return types.ArtifactRef{}, err
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return types.ArtifactRef{}, fmt.Errorf("resolving artifact path: %w", err)
	}
	u := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
	return types.ArtifactRef{Backend: "local", LocalPath: abs, URI: u, URL: u, SHA256: hash, Size: size}, nil
}
