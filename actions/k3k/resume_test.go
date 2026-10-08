package k3k

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// resumeCLI records which lifecycle steps ran and can be made to fail at a chosen step.
type resumeCLI struct {
	calls    []string
	failOn   string
	writeCfg bool
}

func (f *resumeCLI) Create(_ context.Context, item *ClusterState, _ Target, _ *Config) error {
	f.calls = append(f.calls, "create-k3k")
	if f.failOn == "create-k3k" {
		return errors.New("boom")
	}
	if f.writeCfg {
		return os.WriteFile(item.Kubeconfig, []byte("kubeconfig"), 0o600)
	}
	return nil
}

func (f *resumeCLI) Register(_ context.Context, _ *ClusterState, _ []byte) error {
	f.calls = append(f.calls, "register")
	if f.failOn == "register" {
		return errors.New("boom")
	}
	return nil
}

func (f *resumeCLI) Delete(_ context.Context, _ *ClusterState, _ Target) error {
	f.calls = append(f.calls, "delete-k3k")
	return nil
}

func (f *resumeCLI) WaitDeleted(_ context.Context, _ *ClusterState, _ Target) error {
	f.calls = append(f.calls, "wait-k3k-deleted")
	return nil
}

// resumeRancher fails readiness a configurable number of times, mimicking a cluster whose
// agent has not connected yet.
type resumeRancher struct {
	calls        []string
	createdCount int
	readyFails   int
}

func (f *resumeRancher) CreateImportedCluster(_ context.Context, _ *ClusterState, _, _ map[string]string) (string, error) {
	f.calls = append(f.calls, "create-rancher")
	f.createdCount++
	return "c-m-test", nil
}

func (f *resumeRancher) RegistrationManifest(_ context.Context, _ string) ([]byte, error) {
	f.calls = append(f.calls, "manifest")
	return []byte("manifest"), nil
}

func (f *resumeRancher) WaitImportedClusterReady(_ context.Context, _ string) error {
	f.calls = append(f.calls, "wait-rancher-ready")
	if f.readyFails > 0 {
		f.readyFails--
		return errors.New("Rancher cluster did not become ready: Cluster agent is not connected")
	}
	return nil
}

func (f *resumeRancher) DeleteImportedCluster(_ context.Context, _ *ClusterState) error {
	f.calls = append(f.calls, "delete-rancher")
	return nil
}

func resumeFixture(t *testing.T) (*Config, *BatchState, *ClusterState) {
	t.Helper()
	dir := t.TempDir()
	item := &ClusterState{
		Name: "batch-001", Target: "target", Namespace: "k3k-batch-001",
		ArtifactDir: dir, Kubeconfig: filepath.Join(dir, "kubeconfig"),
		RancherProvisioningID: "fleet-default/batch-001", Stage: StagePending,
	}
	state := &BatchState{
		Batch:    "batch",
		Clusters: map[string]*ClusterState{item.Name: item},
		path:     filepath.Join(dir, "state.json"),
	}
	config := &Config{
		Batch: "batch", Parallelism: 1,
		Targets: []Target{{Name: "target", Parallelism: 1}},
	}
	return config, state, item
}

// TestResumeAfterRegistrationFailure is the exact shape of a run that failed while waiting for
// the Rancher agent: the k3k cluster and the Rancher import both already exist, so a rerun must
// skip them and retry only the registration wait.
func TestResumeAfterRegistrationFailure(t *testing.T) {
	config, state, item := resumeFixture(t)
	cli := &resumeCLI{writeCfg: true}
	rancher := &resumeRancher{readyFails: 1}
	orchestrator := &Orchestrator{Config: config, State: state, CLI: cli, Rancher: rancher}

	if err := orchestrator.Create(context.Background()); err == nil {
		t.Fatal("first run should fail while waiting for the agent")
	}
	if item.Stage != StageFailed {
		t.Fatalf("stage = %s, want failed", item.Stage)
	}
	if item.ResumeStage != StageRegistering {
		t.Fatalf("resumeStage = %s, want registering", item.ResumeStage)
	}
	if item.RancherManagementID != "c-m-test" {
		t.Fatalf("management ID was not persisted: %q", item.RancherManagementID)
	}

	cli.calls = nil
	rancher.calls = nil
	if err := orchestrator.Create(context.Background()); err != nil {
		t.Fatalf("resume should succeed: %v", err)
	}
	if item.Stage != StageRegistered {
		t.Fatalf("stage = %s, want registered", item.Stage)
	}

	// The k3k cluster already exists and its kubeconfig is on disk, so it must not be rebuilt.
	for _, call := range cli.calls {
		if call == "create-k3k" {
			t.Fatalf("resume re-created the k3k cluster: %v", cli.calls)
		}
	}
	// The Rancher cluster already exists, so it must not be created a second time.
	if rancher.createdCount != 1 {
		t.Fatalf("Rancher cluster created %d times, want 1", rancher.createdCount)
	}
}

// TestResumeRegeneratesMissingKubeconfig covers cleaning the artifacts directory while keeping
// the state file. The k3k cluster still exists, but the kubeconfig needed for registration is
// gone, so create must run again rather than failing on a missing file.
func TestResumeRegeneratesMissingKubeconfig(t *testing.T) {
	config, state, item := resumeFixture(t)
	item.Stage = StageK3KReady

	cli := &resumeCLI{writeCfg: true}
	rancher := &resumeRancher{}
	orchestrator := &Orchestrator{Config: config, State: state, CLI: cli, Rancher: rancher}

	if err := orchestrator.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cli.calls) == 0 || cli.calls[0] != "create-k3k" {
		t.Fatalf("create should have re-run to regenerate the kubeconfig: %v", cli.calls)
	}
	if item.Stage != StageRegistered {
		t.Fatalf("stage = %s, want registered", item.Stage)
	}
}

// TestRerunAfterSuccessIsANoOp keeps create safe to run repeatedly.
func TestRerunAfterSuccessIsANoOp(t *testing.T) {
	config, state, item := resumeFixture(t)
	cli := &resumeCLI{writeCfg: true}
	rancher := &resumeRancher{}
	orchestrator := &Orchestrator{Config: config, State: state, CLI: cli, Rancher: rancher}

	if err := orchestrator.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	if item.Stage != StageRegistered {
		t.Fatalf("stage = %s, want registered", item.Stage)
	}

	cli.calls = nil
	rancher.calls = nil
	if err := orchestrator.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cli.calls) != 0 || len(rancher.calls) != 0 {
		t.Fatalf("a completed cluster should do no work on rerun: cli=%v rancher=%v", cli.calls, rancher.calls)
	}
}

// TestResumeAfterK3kFailureRebuilds checks the other direction: a cluster that never reached
// the k3k-ready stage must run create again, which is safe because it applies a manifest.
func TestResumeAfterK3kFailureRebuilds(t *testing.T) {
	config, state, item := resumeFixture(t)
	cli := &resumeCLI{failOn: "create-k3k"}
	rancher := &resumeRancher{}
	orchestrator := &Orchestrator{Config: config, State: state, CLI: cli, Rancher: rancher}

	if err := orchestrator.Create(context.Background()); err == nil {
		t.Fatal("first run should fail")
	}
	if item.ResumeStage != StageK3KCreating {
		t.Fatalf("resumeStage = %s, want k3k-creating", item.ResumeStage)
	}
	if len(rancher.calls) != 0 {
		t.Fatalf("Rancher should not be touched after a k3k failure: %v", rancher.calls)
	}

	cli.failOn = ""
	cli.writeCfg = true
	cli.calls = nil
	if err := orchestrator.Create(context.Background()); err != nil {
		t.Fatalf("resume should succeed: %v", err)
	}
	if cli.calls[0] != "create-k3k" {
		t.Fatalf("resume should retry the k3k create: %v", cli.calls)
	}
	if item.Stage != StageRegistered {
		t.Fatalf("stage = %s, want registered", item.Stage)
	}
}

// TestStatePersistsManagementIDAcrossProcesses simulates the process exiting after the Rancher
// cluster was created: the ID must survive in the state file so a rerun adopts it.
func TestStatePersistsManagementIDAcrossProcesses(t *testing.T) {
	config, state, item := resumeFixture(t)
	cli := &resumeCLI{writeCfg: true}
	rancher := &resumeRancher{readyFails: 1}
	orchestrator := &Orchestrator{Config: config, State: state, CLI: cli, Rancher: rancher}

	if err := orchestrator.Create(context.Background()); err == nil {
		t.Fatal("first run should fail")
	}

	reloaded := &BatchState{}
	data, err := os.ReadFile(state.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := unmarshalState(data, reloaded); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Clusters[item.Name].RancherManagementID; got != "c-m-test" {
		t.Fatalf("management ID not persisted to disk: %q", got)
	}
	if got := reloaded.Clusters[item.Name].ResumeStage; got != StageRegistering {
		t.Fatalf("resume stage not persisted to disk: %q", got)
	}
}
