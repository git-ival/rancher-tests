package k3k

import (
	"path/filepath"
	"testing"
)

func TestLoadOrCreateStateAssignments(t *testing.T) {
	dir := t.TempDir()
	config := &Config{
		Batch: "batch", Count: 3, ArtifactsDir: dir, StateFile: filepath.Join(dir, "state.json"),
		Rancher: RancherConfig{Workspace: "fleet-default"},
		Targets: []Target{{Name: "a", Weight: 2}, {Name: "b", Weight: 1}},
	}
	state, err := LoadOrCreateState(config)
	if err != nil {
		t.Fatal(err)
	}
	if state.Clusters["batch-001"].Target != "a" || state.Clusters["batch-002"].Target != "a" || state.Clusters["batch-003"].Target != "b" {
		t.Fatalf("unexpected target assignments: %#v", state.Clusters)
	}
	if state.Clusters["batch-001"].RancherProvisioningID != "fleet-default/batch-001" {
		t.Fatal("provisioning ID was not recorded")
	}
}
