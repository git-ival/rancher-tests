package runconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadResolvesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentic-qa.yaml")
	data := []byte("version: v1\npaths:\n  inputs:\n    featureMapping: config/features.json\n  outputs:\n    state: work/state.json\n    publishedEnvironments: work/published\n")
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
	if got, want := cfg.Paths.Outputs.PublishedEnvironments, filepath.Join(dir, "work/published"); got != want {
		t.Fatalf("publishedEnvironments = %q, want %q", got, want)
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

func TestLoadRequiresS3Bucket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentic-qa.yaml")
	data := []byte("version: v1\nartifacts:\n  backend: s3\n  urlTTL: 1h\npaths:\n  outputs:\n    state: state.json\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted s3 without a bucket")
	}
}

func TestLoadJenkinsEnvironmentJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentic-qa.yaml")
	data := []byte("version: v1\njenkins:\n  environmentJob: go-pit-daily-job-updated\npaths:\n  outputs:\n    state: state.json\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Jenkins.EnvironmentJob != "go-pit-daily-job-updated" {
		t.Fatalf("environmentJob = %q", cfg.Jenkins.EnvironmentJob)
	}
}

func TestLoadQAInfraConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentic-qa.yaml")
	data := []byte("version: v1\nqaInfra:\n  repoUrl: https://github.com/rancher/qa-infra-automation\n  branch: main\npaths:\n  outputs:\n    state: state.json\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.QAInfra.RepoURL == "" || cfg.QAInfra.Branch != "main" {
		t.Fatalf("qaInfra = %#v", cfg.QAInfra)
	}
}

func TestLoadTestsRepositoryConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentic-qa.yaml")
	data := []byte("version: v1\ntests:\n  repoUrl: https://github.com/rancher/tests\n  branch: main\npaths:\n  outputs:\n    state: state.json\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tests.RepoURL != "https://github.com/rancher/tests" || cfg.Tests.Branch != "main" {
		t.Fatalf("tests = %#v", cfg.Tests)
	}
}
