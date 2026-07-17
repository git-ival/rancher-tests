package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/state"
	"github.com/rancher/tests/internal/agenticqa/types"
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
		if err := prepareRunInputs(cmd.Context()); err != nil {
			return err
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
			if err := executeWorkflowStage(cmd.Context(), tracker, stage); err != nil {
				return err
			}
		}
		return saveJSON(runConfig.Paths.Outputs.Summary, map[string]any{
			"repo": runConfig.Source.Repo, "pr": runConfig.Source.PR,
			"completed_at": time.Now().UTC().Format(time.RFC3339),
		})
	},
}

func prepareRunInputs(ctx context.Context) error {
	clearMissingOptionalRunInputs()
	problems := runPreflightProblems()
	for name, path := range map[string]string{
		"paths.outputs.identifiedTests":      runConfig.Paths.Outputs.IdentifiedTests,
		"paths.outputs.environmentPlan":      runConfig.Paths.Outputs.EnvironmentPlan,
		"paths.outputs.environmentArtifacts": runConfig.Paths.Outputs.EnvironmentArtifacts,
		"paths.outputs.setupEnvironments":    runConfig.Paths.Outputs.SetupEnvironments,
		"paths.outputs.triggeredJobs":        runConfig.Paths.Outputs.TriggeredJobs,
		"paths.outputs.completedJobs":        runConfig.Paths.Outputs.CompletedJobs,
		"paths.outputs.triageResults":        runConfig.Paths.Outputs.TriageResults,
		"paths.outputs.defectActions":        runConfig.Paths.Outputs.DefectActions,
		"paths.outputs.configActions":        runConfig.Paths.Outputs.ConfigActions,
		"paths.outputs.summary":              runConfig.Paths.Outputs.Summary,
	} {
		if path == "" {
			problems = append(problems, name+" is required")
		}
	}
	if (runConfig.Artifacts.Backend == "" || runConfig.Artifacts.Backend == "local") && runConfig.Paths.Outputs.PublishedEnvironments == "" {
		problems = append(problems, "paths.outputs.publishedEnvironments is required for local artifact publishing")
	}
	if os.Getenv(githubTokenEnvVar) == "" {
		problems = append(problems, githubTokenEnvVar+" is not set")
	}
	featureMappingMissing := !isFile(runConfig.Paths.Inputs.FeatureMapping) && !isFile(runConfig.Paths.Outputs.FeatureMapping)
	if (!dryRun || featureMappingMissing) && os.Getenv(qaseApiTokenEnvVar) == "" {
		problems = append(problems, qaseApiTokenEnvVar+" is not set")
	}
	if !dryRun && !localTest {
		jenkinsURL := os.Getenv(jenkinsURLEnvVar)
		if jenkinsURL == "" {
			jenkinsURL = runConfig.Jenkins.URL
		}
		if jenkinsURL == "" {
			problems = append(problems, jenkinsURLEnvVar+" or jenkins.url is required")
		}
		if activeJenkinsUser() == "" {
			problems = append(problems, jenkinsUserEnvVar+" or jenkins.user is required")
		}
		if os.Getenv(jenkinsTokenEnvVar) == "" {
			problems = append(problems, jenkinsTokenEnvVar+" is not set")
		}
	}
	switch provider {
	case llmProviderVertexAI:
		creds := os.Getenv(googleApplicationCredentialsEnvVar)
		if creds == "" {
			problems = append(problems, googleApplicationCredentialsEnvVar+" is not set")
		} else if !isFile(creds) {
			problems = append(problems, fmt.Sprintf("%s does not reference a file: %s", googleApplicationCredentialsEnvVar, creds))
		}
		if vertexProject == "" {
			problems = append(problems, "llm.vertexProject is required for vertex-ai")
		}
	case llmProviderClaudeDirect:
		if os.Getenv(claudeAPIKeyEnvVar) == "" {
			problems = append(problems, claudeAPIKeyEnvVar+" is not set")
		}
	default:
		problems = append(problems, fmt.Sprintf("unsupported llm.provider %q", provider))
	}
	if !isFile(runConfig.Paths.Inputs.FeatureMapping) && isFile(runConfig.Paths.Outputs.FeatureMapping) {
		runConfig.Paths.Inputs.FeatureMapping = runConfig.Paths.Outputs.FeatureMapping
	}
	if !isFile(runConfig.Paths.Inputs.TriggerMapping) && isFile(runConfig.Paths.Outputs.TriggerMapping) {
		runConfig.Paths.Inputs.TriggerMapping = runConfig.Paths.Outputs.TriggerMapping
	}
	if !isFile(runConfig.Paths.Inputs.FeatureMapping) {
		if !isDir(runConfig.Paths.Inputs.ValidationDir) {
			problems = append(problems, fmt.Sprintf("feature mapping is missing and paths.inputs.validationDir is not a directory: %s", runConfig.Paths.Inputs.ValidationDir))
		}
		if runConfig.Paths.Inputs.ActionsDir != "" && !isDir(runConfig.Paths.Inputs.ActionsDir) {
			problems = append(problems, fmt.Sprintf("feature mapping is missing and paths.inputs.actionsDir is not a directory: %s", runConfig.Paths.Inputs.ActionsDir))
		}
		if runConfig.Paths.Outputs.FeatureMapping == "" {
			problems = append(problems, "feature mapping is missing and paths.outputs.featureMapping is empty")
		}
	}
	if !isFile(runConfig.Paths.Inputs.TriggerMapping) {
		if !isDir(runConfig.Paths.Inputs.JJBDir) {
			problems = append(problems, fmt.Sprintf("trigger mapping is missing and paths.inputs.jjbDir is not a directory: %s", runConfig.Paths.Inputs.JJBDir))
		}
		if runConfig.Paths.Outputs.TriggerMapping == "" {
			problems = append(problems, "trigger mapping is missing and paths.outputs.triggerMapping is empty")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("run preflight failed:\n- %s", strings.Join(problems, "\n- "))
	}

	if !isFile(runConfig.Paths.Inputs.FeatureMapping) {
		logrus.Infof("Feature mapping not found; generating %s", runConfig.Paths.Outputs.FeatureMapping)
		genFeatureMapValidationDir = runConfig.Paths.Inputs.ValidationDir
		genFeatureMapActionsDir = runConfig.Paths.Inputs.ActionsDir
		genFeatureMapOutputFile = runConfig.Paths.Outputs.FeatureMapping
		if err := runGenerateFeatureMap(ctx); err != nil {
			return fmt.Errorf("bootstrapping feature mapping: %w", err)
		}
		runConfig.Paths.Inputs.FeatureMapping = runConfig.Paths.Outputs.FeatureMapping
	}
	if !isFile(runConfig.Paths.Inputs.TriggerMapping) {
		logrus.Infof("Trigger mapping not found; generating %s", runConfig.Paths.Outputs.TriggerMapping)
		genTriggerMapJJBDir = runConfig.Paths.Inputs.JJBDir
		genTriggerMapOutputFile = runConfig.Paths.Outputs.TriggerMapping
		if err := runGenerateTriggerMap(); err != nil {
			return fmt.Errorf("bootstrapping trigger mapping: %w", err)
		}
		runConfig.Paths.Inputs.TriggerMapping = runConfig.Paths.Outputs.TriggerMapping
	}
	return nil
}

func runPreflightProblems() []string {
	var problems []string
	if runConfig.Source.Repo == "" {
		problems = append(problems, "source.repo is required")
	}
	if runConfig.Source.PR <= 0 {
		problems = append(problems, "source.pr must be a positive pull request number")
	}
	if !isDir(runConfig.Paths.Inputs.TestRepoRoot) {
		problems = append(problems, fmt.Sprintf("paths.inputs.testRepoRoot is not a directory: %s", runConfig.Paths.Inputs.TestRepoRoot))
	}
	return problems
}

func clearMissingOptionalRunInputs() {
	optional := []*string{
		&runConfig.Paths.Inputs.PipelineEnv,
		&runConfig.Paths.Inputs.TriageFramework,
		&runConfig.Paths.Inputs.AdditionalContext,
		&runConfig.Paths.Inputs.ChartsDir,
	}
	for _, path := range optional {
		if *path != "" {
			if _, err := os.Stat(*path); os.IsNotExist(err) {
				logrus.Debugf("Optional run input does not exist: %s", *path)
				*path = ""
			}
		}
	}
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func isDir(path string) bool {
	info, err := os.Stat(filepath.Clean(path))
	return err == nil && info.IsDir()
}

type workflowStage struct {
	name    string
	command *cobra.Command
	output  string
}

func executeWorkflowStage(ctx context.Context, tracker *state.Tracker, stage workflowStage) error {
	s, err := tracker.Load()
	if err != nil {
		return err
	}
	if current, ok := s.Stages[stage.name]; ok && current.Status == stageSucceeded && validStageOutput(stage.name, stage.output) {
		return nil
	}
	if err := applyRunConfig(stage.command, runConfig); err != nil {
		return err
	}
	if err := markStage(tracker, stage.name, stageRunning, ""); err != nil {
		return err
	}
	stage.command.SetContext(ctx)
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

func validStageOutput(stage, path string) bool {
	if !validJSONFile(path) {
		return false
	}
	if stage != waitCommandName || runConfig == nil {
		return true
	}
	var triggered types.TriggeredJobs
	if err := loadJSON(runConfig.Paths.Outputs.TriggeredJobs, &triggered); err != nil {
		return false
	}
	var completed types.CompletedJobs
	if err := loadJSON(path, &completed); err != nil {
		return false
	}
	return len(completed.Completed)+len(completed.Failed) == len(triggered.Jobs)
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
