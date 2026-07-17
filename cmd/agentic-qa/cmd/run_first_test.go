package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/runconfig"
	"github.com/rancher/tests/internal/agenticqa/state"
)

func TestClearMissingOptionalRunInputs(t *testing.T) {
	dir := t.TempDir()
	runConfig = &runconfig.Config{Paths: runconfig.PathsConfig{Inputs: runconfig.InputPaths{
		PipelineEnv:       filepath.Join(dir, "pipeline_env.json"),
		TriageFramework:   filepath.Join(dir, "triage.json"),
		AdditionalContext: filepath.Join(dir, "context.txt"),
		ChartsDir:         filepath.Join(dir, "charts"),
	}}}
	t.Cleanup(func() { runConfig = nil })

	clearMissingOptionalRunInputs()
	if runConfig.Paths.Inputs.PipelineEnv != "" || runConfig.Paths.Inputs.TriageFramework != "" || runConfig.Paths.Inputs.AdditionalContext != "" || runConfig.Paths.Inputs.ChartsDir != "" {
		t.Fatalf("optional inputs were not cleared: %+v", runConfig.Paths.Inputs)
	}
}

func TestRunPreflightReportsMultipleProblems(t *testing.T) {
	runConfig = &runconfig.Config{Paths: runconfig.PathsConfig{Inputs: runconfig.InputPaths{TestRepoRoot: filepath.Join(t.TempDir(), "missing")}}}
	t.Cleanup(func() { runConfig = nil })

	problems := strings.Join(runPreflightProblems(), "\n")
	for _, want := range []string{"source.repo", "source.pr", "testRepoRoot"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems %q do not contain %q", problems, want)
		}
	}
}

func TestMissingConfiguredPipelineEnvUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	env, err := loadPipelineEnv(path, false)
	if err != nil {
		t.Fatalf("loadPipelineEnv: %v", err)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil fallback", env)
	}
	if _, err := loadPipelineEnv(path, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit missing config error = %v", err)
	}
}

func TestExecuteWorkflowStagePropagatesContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "value")
	runConfig = &runconfig.Config{}
	t.Cleanup(func() { runConfig = nil })
	output := filepath.Join(t.TempDir(), "output.json")
	tracker := state.NewTracker(filepath.Join(t.TempDir(), "state.json"))
	if err := tracker.Ensure(); err != nil {
		t.Fatal(err)
	}

	child := &cobra.Command{Use: "child"}
	child.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Context() != ctx {
			t.Fatal("child command did not receive workflow context")
		}
		return saveJSON(output, map[string]bool{"ok": true})
	}

	if err := executeWorkflowStage(ctx, tracker, workflowStage{name: "child", command: child, output: output}); err != nil {
		t.Fatalf("executeWorkflowStage: %v", err)
	}
}

func TestValidStageOutputRejectsMissingCompletedJobs(t *testing.T) {
	dir := t.TempDir()
	triggeredPath := filepath.Join(dir, "triggered.json")
	completedPath := filepath.Join(dir, "completed.json")
	runConfig = &runconfig.Config{Paths: runconfig.PathsConfig{Outputs: runconfig.OutputPaths{TriggeredJobs: triggeredPath}}}
	t.Cleanup(func() { runConfig = nil })

	if err := saveJSON(triggeredPath, map[string]any{"jobs": []map[string]string{{"job_name": "job", "status": "queued"}}}); err != nil {
		t.Fatal(err)
	}
	if err := saveJSON(completedPath, map[string]any{"completed": nil, "failed": nil}); err != nil {
		t.Fatal(err)
	}
	if validStageOutput(waitCommandName, completedPath) {
		t.Fatal("wait output with missing jobs was accepted")
	}
}
