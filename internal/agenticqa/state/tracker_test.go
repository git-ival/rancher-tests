package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCreatesStateAndParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentic-qa", "pipeline_state.json")
	tracker := NewTracker(path)

	if err := tracker.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file was not created: %v", err)
	}

	state, err := tracker.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(state.QaseRuns) != 0 || len(state.QaseDefects) != 0 || len(state.GithubIssues) != 0 || len(state.GithubPRs) != 0 {
		t.Fatalf("state is not empty: %#v", state)
	}
}

func TestAddQaseRunCreatesMissingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentic-qa", "pipeline_state.json")
	tracker := NewTracker(path)

	if err := tracker.AddQaseRun("RM", 7181); err != nil {
		t.Fatalf("AddQaseRun: %v", err)
	}
	state, err := tracker.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(state.QaseRuns) != 1 || state.QaseRuns[0].Project != "RM" || state.QaseRuns[0].RunID != 7181 {
		t.Fatalf("QaseRuns = %#v", state.QaseRuns)
	}
}
