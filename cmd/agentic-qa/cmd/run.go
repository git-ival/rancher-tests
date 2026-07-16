package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/state"
)

const (
	stageSucceeded = "succeeded"
	stageRunning   = "running"
	stageFailed    = "failed"
)

func init() { rootCmd.AddCommand(runCmd) }

var runCmd = &cobra.Command{
	Use:   runCommandName,
	Short: "Run or resume the Agentic QA workflow",
	RunE: func(cmd *cobra.Command, args []string) error {
		if runConfig == nil {
			return fmt.Errorf("--%s is required for run", configFlag)
		}
		if stateFile == "" {
			return fmt.Errorf("paths.outputs.state is required")
		}
		tracker := state.NewTracker(stateFile)
		if err := tracker.Ensure(); err != nil {
			return err
		}
		if err := tracker.Update(func(s *state.PipelineState) error {
			if s.Version == "" {
				s.Version = "v1"
			}
			if s.Run.ID == "" {
				s.Run.ID = fmt.Sprintf("pr-%d-%d", runConfig.Source.PR, time.Now().UTC().Unix())
				s.Run.Repo = runConfig.Source.Repo
				s.Run.PR = runConfig.Source.PR
			}
			return nil
		}); err != nil {
			return err
		}

		stages := []workflowStage{
			{name: identifyCommandName, command: identifyCmd, output: runConfig.Paths.Outputs.IdentifiedTests},
			{name: planEnvironmentCommandName, command: planEnvironmentCmd, output: runConfig.Paths.Outputs.EnvironmentPlan},
			{name: setupEnvironmentCommandName, command: setupEnvCmd, output: runConfig.Paths.Outputs.SetupEnvironments},
			{name: triggerCommandName, command: triggerCmd, output: runConfig.Paths.Outputs.TriggeredJobs},
			{name: waitCommandName, command: waitCmd, output: runConfig.Paths.Outputs.CompletedJobs},
			{name: analyzeCommandName, command: analyzeCmd, output: runConfig.Paths.Outputs.TriageResults},
			{name: defectsCommandName, command: defectsCmd, output: runConfig.Paths.Outputs.DefectActions},
			{name: configFailuresCommandName, command: configFailuresCmd, output: runConfig.Paths.Outputs.ConfigActions},
		}
		if runConfig.Execution.CleanupAfterRun {
			stages = append(stages, workflowStage{name: cleanupCommandName, command: cleanupCmd, output: runConfig.Paths.Outputs.CleanupResult})
		}
		for _, stage := range stages {
			if err := executeWorkflowStage(tracker, stage); err != nil {
				return err
			}
		}
		return saveJSON(runConfig.Paths.Outputs.Summary, map[string]any{
			"repo": runConfig.Source.Repo, "pr": runConfig.Source.PR,
			"completed_at": time.Now().UTC().Format(time.RFC3339),
		})
	},
}

type workflowStage struct {
	name    string
	command *cobra.Command
	output  string
}

func executeWorkflowStage(tracker *state.Tracker, stage workflowStage) error {
	s, err := tracker.Load()
	if err != nil {
		return err
	}
	if current, ok := s.Stages[stage.name]; ok && current.Status == stageSucceeded && validJSONFile(stage.output) {
		return nil
	}
	if err := applyRunConfig(stage.command, runConfig); err != nil {
		return err
	}
	if err := markStage(tracker, stage.name, stageRunning, ""); err != nil {
		return err
	}
	if err := stage.command.RunE(stage.command, nil); err != nil {
		_ = markStage(tracker, stage.name, stageFailed, err.Error())
		return fmt.Errorf("%s: %w", stage.name, err)
	}
	if !validJSONFile(stage.output) {
		err := fmt.Errorf("%s did not produce valid JSON at %s", stage.name, stage.output)
		_ = markStage(tracker, stage.name, stageFailed, err.Error())
		return err
	}
	return markStage(tracker, stage.name, stageSucceeded, "")
}

func markStage(tracker *state.Tracker, name, status, message string) error {
	return tracker.Update(func(s *state.PipelineState) error {
		if s.Stages == nil {
			s.Stages = map[string]state.StageState{}
		}
		s.Stages[name] = state.StageState{Status: status, UpdatedAt: time.Now().UTC().Format(time.RFC3339), Error: message}
		return nil
	})
}

func validJSONFile(path string) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var value any
	return json.Unmarshal(data, &value) == nil
}
