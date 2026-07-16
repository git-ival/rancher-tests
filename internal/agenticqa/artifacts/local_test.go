package artifacts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalPublisherCopiesAndHashes(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	if err := os.WriteFile(source, []byte("rancher:\n  host: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &localPublisher{root: filepath.Join(dir, "published")}
	ref, err := p.Publish(context.Background(), source, "environments/all/cattle-config.yaml")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if ref.SHA256 == "" || ref.Size == 0 || ref.URI == "" {
		t.Fatalf("incomplete ref: %#v", ref)
	}
	if _, err := os.Stat(ref.LocalPath); err != nil {
		t.Fatalf("published file: %v", err)
	}
}

func TestLocalPublisherRejectsTraversal(t *testing.T) {
	p := &localPublisher{root: t.TempDir()}
	if _, err := p.Publish(context.Background(), "unused", "../outside"); err == nil {
		t.Fatal("Publish accepted traversal")
	}
}
