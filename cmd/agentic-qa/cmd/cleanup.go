package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/artifacts"
	ghclient "github.com/rancher/tests/internal/agenticqa/github"
	"github.com/rancher/tests/internal/agenticqa/qase"
	"github.com/rancher/tests/internal/agenticqa/state"
	"github.com/rancher/tests/internal/agenticqa/types"
)

var (
	cleanupOutputFile string
)

func init() {
	f := cleanupCmd.Flags()
	f.StringVar(&cleanupOutputFile, outputFileFlag, "", "Path to write cleanup_result.json (required)")

	_ = cleanupCmd.MarkFlagRequired(outputFileFlag)

	rootCmd.AddCommand(cleanupCmd)
}

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Clean up pipeline-created resources",
	Long:  `Deletes Qase runs/defects and closes GitHub issues/PRs tracked in the pipeline state file.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		if stateFile == "" {
			return fmt.Errorf("--state-file is required for cleanup")
		}

		tracker := state.NewTracker(stateFile)
		pipelineState, err := tracker.Load()
		if err != nil {
			return fmt.Errorf("loading pipeline state: %w", err)
		}

		ghToken := os.Getenv(githubTokenEnvVar)
		gh := ghclient.NewClient(ghToken)

		mcpClient := qase.NewMCPClient(mcpURL)
		qaseToken := os.Getenv(qaseApiTokenEnvVar)
		qaseClient := qase.NewClient(qaseToken)

		result := types.CleanupResult{}
		if runConfig != nil {
			var remaining []state.ArtifactState
			for _, artifact := range pipelineState.Artifacts {
				if dryRun {
					remaining = append(remaining, artifact)
					continue
				}
				ref := types.ArtifactRef{Backend: artifact.Backend, URI: artifact.URI, Bucket: artifact.Bucket, Key: artifact.Key}
				if artifact.Backend == "local" {
					ref.LocalPath = strings.TrimPrefix(artifact.URI, "file://")
				}
				if err := artifacts.Delete(ctx, runConfig.Artifacts, ref); err != nil && !os.IsNotExist(err) {
					result.Errors = append(result.Errors, fmt.Sprintf("artifact %s: %v", artifact.URI, err))
					remaining = append(remaining, artifact)
				}
			}
			if !dryRun {
				if err := tracker.Update(func(s *state.PipelineState) error {
					s.Artifacts = remaining
					return nil
				}); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("updating artifact state: %v", err))
				}
			}
		}

		// Delete Qase runs
		for _, run := range pipelineState.QaseRuns {
			if dryRun {
				logrus.Infof("Dry-run: would delete Qase run %d in %s", run.RunID, run.Project)
				result.QaseRunsDeleted++
				continue
			}

			var err error
			if mcpClient.IsConfigured() {
				err = mcpClient.DeleteTestRun(ctx, run.Project, run.RunID)
			} else if qaseToken != "" {
				err = qaseClient.DeleteTestRun(ctx, run.Project, run.RunID)
			} else {
				err = fmt.Errorf("no Qase client configured")
			}

			if err != nil {
				logrus.Warnf("Failed to delete Qase run %d: %v", run.RunID, err)
				result.Errors = append(result.Errors, fmt.Sprintf("qase run %d: %v", run.RunID, err))
			} else {
				result.QaseRunsDeleted++
				logrus.Infof("Deleted Qase run %d", run.RunID)
			}
		}

		// Delete Qase defects
		for _, defect := range pipelineState.QaseDefects {
			if dryRun {
				logrus.Infof("Dry-run: would delete Qase defect %d in %s", defect.DefectID, defect.Project)
				result.QaseDefectsDeleted++
				continue
			}

			var err error
			if mcpClient.IsConfigured() {
				err = mcpClient.DeleteDefect(ctx, defect.Project, defect.DefectID)
			} else if qaseToken != "" {
				err = qaseClient.DeleteDefect(ctx, defect.Project, defect.DefectID)
			} else {
				err = fmt.Errorf("no Qase client configured")
			}

			if err != nil {
				logrus.Warnf("Failed to delete Qase defect %d: %v", defect.DefectID, err)
				result.Errors = append(result.Errors, fmt.Sprintf("qase defect %d: %v", defect.DefectID, err))
			} else {
				result.QaseDefectsDeleted++
				logrus.Infof("Deleted Qase defect %d", defect.DefectID)
			}
		}

		// Close GitHub issues
		if ghToken != "" {
			for _, issue := range pipelineState.GithubIssues {
				if dryRun {
					logrus.Infof("Dry-run: would close GitHub issue %s#%d", issue.Repo, issue.IssueNumber)
					result.GithubIssuesClosed++
					continue
				}

				owner, repo, err := ghclient.ParseRepo(issue.Repo)
				if err != nil {
					logrus.Warnf("Invalid repo %s: %v", issue.Repo, err)
					result.Errors = append(result.Errors, fmt.Sprintf("github issue %s#%d: %v", issue.Repo, issue.IssueNumber, err))
					continue
				}

				if err := gh.CloseIssue(ctx, owner, repo, issue.IssueNumber); err != nil {
					logrus.Warnf("Failed to close issue %s#%d: %v", issue.Repo, issue.IssueNumber, err)
					result.Errors = append(result.Errors, fmt.Sprintf("github issue %s#%d: %v", issue.Repo, issue.IssueNumber, err))
				} else {
					result.GithubIssuesClosed++
				}
			}

			// Close GitHub PRs
			for _, pr := range pipelineState.GithubPRs {
				if dryRun {
					logrus.Infof("Dry-run: would close GitHub PR %s#%d", pr.Repo, pr.PRNumber)
					result.GithubPRsClosed++
					continue
				}

				owner, repo, err := ghclient.ParseRepo(pr.Repo)
				if err != nil {
					logrus.Warnf("Invalid repo %s: %v", pr.Repo, err)
					result.Errors = append(result.Errors, fmt.Sprintf("github PR %s#%d: %v", pr.Repo, pr.PRNumber, err))
					continue
				}

				if err := gh.ClosePR(ctx, owner, repo, pr.PRNumber); err != nil {
					logrus.Warnf("Failed to close PR %s#%d: %v", pr.Repo, pr.PRNumber, err)
					result.Errors = append(result.Errors, fmt.Sprintf("github PR %s#%d: %v", pr.Repo, pr.PRNumber, err))
				} else {
					result.GithubPRsClosed++
				}
			}
		}

		if err := saveJSON(cleanupOutputFile, result); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		logrus.Infof("Cleanup complete: %d runs, %d defects, %d issues, %d PRs deleted/closed (%d errors) → %s",
			result.QaseRunsDeleted, result.QaseDefectsDeleted, result.GithubIssuesClosed, result.GithubPRsClosed,
			len(result.Errors), cleanupOutputFile)
		return nil
	},
}
