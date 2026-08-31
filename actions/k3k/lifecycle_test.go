package k3k

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeCLI struct {
	calls []string
}

func (f *fakeCLI) Create(_ context.Context, item *ClusterState, _ Target, _ *Config) error {
	f.calls = append(f.calls, "create-k3k")
	return os.WriteFile(item.Kubeconfig, []byte("kubeconfig"), 0o600)
}

func (f *fakeCLI) Register(_ context.Context, _ *ClusterState, _ []byte) error {
	f.calls = append(f.calls, "register")
	return nil
}

func (f *fakeCLI) Delete(_ context.Context, _ *ClusterState, _ Target) error {
	f.calls = append(f.calls, "delete-k3k")
	return nil
}

func (f *fakeCLI) WaitDeleted(_ context.Context, _ *ClusterState, _ Target) error {
	f.calls = append(f.calls, "wait-k3k-deleted")
	return nil
}

type fakeRancher struct {
	calls []string
}

func (f *fakeRancher) CreateImportedCluster(_ context.Context, item *ClusterState, _ map[string]string) error {
	f.calls = append(f.calls, "create-rancher")
	item.RancherManagementID = "c-m-test"
	return nil
}

func (f *fakeRancher) RegistrationManifest(_ context.Context, _ string) ([]byte, error) {
	f.calls = append(f.calls, "manifest")
	return []byte("manifest"), nil
}

func (f *fakeRancher) WaitImportedClusterReady(_ context.Context, _ string) error {
	f.calls = append(f.calls, "wait-rancher-ready")
	return nil
}

func (f *fakeRancher) DeleteImportedCluster(_ context.Context, _ *ClusterState) error {
	f.calls = append(f.calls, "delete-rancher")
	return nil
}

func TestLifecycleCreateAndDeleteOrdering(t *testing.T) {
	dir := t.TempDir()
	item := &ClusterState{
		Name: "batch-001", Target: "target", Namespace: "k3k-batch-001",
		ArtifactDir: dir, Kubeconfig: filepath.Join(dir, "kubeconfig"),
		RancherProvisioningID: "fleet-default/batch-001", Stage: StagePending,
	}
	state := &BatchState{Batch: "batch", Clusters: map[string]*ClusterState{item.Name: item}, path: filepath.Join(dir, "state.json")}
	cli := &fakeCLI{}
	rancher := &fakeRancher{}
	orchestrator := &Orchestrator{
		Config: &Config{Batch: "batch", Parallelism: 1, Targets: []Target{{Name: "target", Parallelism: 1}}},
		State:  state, CLI: cli, Rancher: rancher,
	}

	if err := orchestrator.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	if item.Stage != StageRegistered {
		t.Fatalf("unexpected stage %s", item.Stage)
	}
	if got := rancher.calls; len(got) != 3 || got[0] != "create-rancher" || got[1] != "manifest" || got[2] != "wait-rancher-ready" {
		t.Fatalf("unexpected Rancher create calls: %v", got)
	}

	if err := orchestrator.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if item.Stage != StageDeleted {
		t.Fatalf("unexpected stage %s", item.Stage)
	}
	if rancher.calls[3] != "delete-rancher" || cli.calls[2] != "delete-k3k" || cli.calls[3] != "wait-k3k-deleted" {
		t.Fatalf("unexpected deletion order: Rancher=%v CLI=%v", rancher.calls, cli.calls)
	}
}

func TestFailedStateRetainsResumeStage(t *testing.T) {
	item := &ClusterState{Name: "cluster", Stage: StageRegistering}
	state := &BatchState{Clusters: map[string]*ClusterState{"cluster": item}, path: filepath.Join(t.TempDir(), "state.json")}
	if err := state.Update("cluster", StageFailed, errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	if item.ResumeStage != StageRegistering || item.LastError != "failed" {
		t.Fatalf("failure did not retain resume stage: %#v", item)
	}
}
