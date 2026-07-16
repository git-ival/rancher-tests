package runconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadResolvesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentic-qa.yaml")
	data := []byte("version: v1\npaths:\n  inputs:\n    featureMapping: config/features.json\n  outputs:\n    state: work/state.json\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Paths.Inputs.FeatureMapping, filepath.Join(dir, "config/features.json"); got != want {
		t.Fatalf("featureMapping = %q, want %q", got, want)
	}
	if got, want := cfg.Paths.Outputs.State, filepath.Join(dir, "work/state.json"); got != want {
		t.Fatalf("state = %q, want %q", got, want)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentic-qa.yaml")
	if err := os.WriteFile(path, []byte("version: v1\nunknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted an unknown field")
	}
}
