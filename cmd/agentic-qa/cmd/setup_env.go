package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/artifacts"
	"github.com/rancher/tests/internal/agenticqa/state"
	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	setupEnvCommandName = "setup-env"
	setupEnvPlanFlag    = "environment-plan"
	setupEnvOutputFlag  = "output-file"
)

var (
	setupEnvPlan   string
	setupEnvOutput string
)

func init() {
	f := setupEnvCmd.Flags()
	f.StringVar(&setupEnvPlan, setupEnvPlanFlag, "", "Path to environment_plan.json (required)")
	f.StringVar(&setupEnvOutput, setupEnvOutputFlag, "", "Path to write setup_environments.json (required)")
	_ = setupEnvCmd.MarkFlagRequired(setupEnvPlanFlag)
	_ = setupEnvCmd.MarkFlagRequired(setupEnvOutputFlag)
	rootCmd.AddCommand(setupEnvCmd)
}

var setupEnvCmd = &cobra.Command{
	Use:   setupEnvCommandName,
	Short: "Prepare and publish test environment inputs",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSetupEnv(cmd.Context())
	},
}

func runSetupEnv(ctx context.Context) error {
	if runConfig == nil {
		return fmt.Errorf("--%s is required for %s", configFlag, setupEnvCommandName)
	}
	var plan types.EnvironmentPlan
	if err := loadJSON(setupEnvPlan, &plan); err != nil {
		return fmt.Errorf("loading environment plan: %w", err)
	}
	publicationRoot := filepath.Join(runConfig.Paths.Workspace, "published")
	publisher, err := artifacts.New(ctx, runConfig.Artifacts, publicationRoot)
	if err != nil {
		return err
	}

	result := types.SetupEnvironments{PreparedAt: time.Now().UTC().Format(time.RFC3339)}
	seen := map[string]string{}
	for _, group := range plan.Groups {
		if group.CattleConfigPath == "" {
			if !group.Cluster.Downstream {
				continue
			}
			return fmt.Errorf("environment group %q has no cattle config", group.Name)
		}
		name := sanitizeGroupName(group.Name)
		if prior, ok := seen[name]; ok && prior != group.Name {
			return fmt.Errorf("environment names %q and %q collide as %q", prior, group.Name, name)
		}
		seen[name] = group.Name
		artifact, err := publisher.Publish(ctx, group.CattleConfigPath, filepath.ToSlash(filepath.Join("environments", name, "cattle-config.yaml")))
		if err != nil {
			return fmt.Errorf("publishing environment %q: %w", group.Name, err)
		}
		result.Groups = append(result.Groups, types.SetupEnvironment{
			Name: group.Name, JenkinsJobs: group.JenkinsJobs, TestFiles: group.TestFiles,
			CattleConfigPath: group.CattleConfigPath, Artifacts: []types.ArtifactRef{artifact},
		})
	}
	if len(result.Groups) == 0 {
		return fmt.Errorf("environment plan contains no publishable groups")
	}
	if err := saveJSON(setupEnvOutput, result); err != nil {
		return fmt.Errorf("writing setup environments: %w", err)
	}
	if stateFile != "" {
		tracker := state.NewTracker(stateFile)
		if err := tracker.Update(func(s *state.PipelineState) error {
			s.Environments = nil
			for _, group := range result.Groups {
				ref := group.Artifacts[0]
				s.Environments = append(s.Environments, state.EnvironmentState{Name: group.Name, JenkinsJobs: group.JenkinsJobs, ArtifactURI: ref.URI, SHA256: ref.SHA256})
				s.Artifacts = append(s.Artifacts, state.ArtifactState{Backend: ref.Backend, URI: ref.URI, Bucket: ref.Bucket, Key: ref.Key})
			}
			return nil
		}); err != nil {
			return fmt.Errorf("tracking environments: %w", err)
		}
	}
	return nil
}

func findSetupEnvironment(setup types.SetupEnvironments, job string) (*types.SetupEnvironment, error) {
	var match *types.SetupEnvironment
	for i := range setup.Groups {
		group := &setup.Groups[i]
		for _, candidate := range group.JenkinsJobs {
			if candidate != job {
				continue
			}
			if match != nil {
				return nil, fmt.Errorf("job %q belongs to multiple environment groups", job)
			}
			match = group
		}
	}
	if match == nil && len(setup.Groups) == 1 && strings.EqualFold(setup.Groups[0].Name, types.PlanGroupNameAll) {
		match = &setup.Groups[0]
	}
	if match == nil {
		return nil, fmt.Errorf("job %q has no environment group", job)
	}
	return match, nil
}
