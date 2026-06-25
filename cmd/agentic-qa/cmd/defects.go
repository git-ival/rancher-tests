package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	ghclient "github.com/rancher/tests/internal/agenticqa/github"
	"github.com/rancher/tests/internal/agenticqa/qase"
	"github.com/rancher/tests/internal/agenticqa/state"
	"github.com/rancher/tests/internal/agenticqa/triage"
	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	defectsProductRepoFlag      = "product-repo"
	defectsAutoCreateIssuesFlag = "auto-create-issues"
	defectsUseCopilotFlag       = "use-copilot"

	defectSeverityPrefix   = "severity/"
	defectNormalSeverity   = "normal"
	defectMinorSeverity    = "minor"
	defectMajorSeverity    = "major"
	defectCriticalSeverity = "critical"
	defectBlockerSeverity  = "blocker"
	defectSeverityTrivial  = "trivial"
)

var (
	defectsTriageResults   string
	defectsPipelineConfig  string
	defectsProductRepo     string
	defectsTestsRepo       string
	defectsPRNumber        int
	defectsAutoCreateIssue bool
	defectsAutoCreatePRs   bool
	defectsUseCopilot      bool
	defectsOutputFile      string
)

func init() {
	f := defectsCmd.Flags()
	f.StringVar(&defectsTriageResults, triageResultsFlag, "", "Path to triage_results.json (required)")
	f.StringVar(&defectsPipelineConfig, pipelineConfigFlag, "", "Path to pipeline config")
	f.StringVar(&defectsProductRepo, defectsProductRepoFlag, defaultProductRepo, "Product repository (owner/repo)")
	f.StringVar(&defectsTestsRepo, testsRepoFlag, defaultTestsRepo, "Tests repository (owner/repo)")
	f.IntVar(&defectsPRNumber, prNumberFlag, 0, "Pull request number")
	f.BoolVar(&defectsAutoCreateIssue, defectsAutoCreateIssuesFlag, false, "Automatically create GitHub issues")
	f.BoolVar(&defectsAutoCreatePRs, autoCreatePRsFlag, false, "Automatically create GitHub PRs")
	f.BoolVar(&defectsUseCopilot, defectsUseCopilotFlag, true, "Use Copilot for fix generation")
	f.StringVar(&defectsOutputFile, outputFileFlag, "", "Path to write defect_actions.json (required)")

	_ = defectsCmd.MarkFlagRequired(triageResultsFlag)
	_ = defectsCmd.MarkFlagRequired(outputFileFlag)

	rootCmd.AddCommand(defectsCmd)
}

var defectsCmd = &cobra.Command{
	Use:   "defects",
	Short: "Handle defects",
	Long:  `Creates GitHub issues, Qase defects, and optionally generates fix PRs for identified defects.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		var triageResults types.TriageResults
		if err := loadJSON(defectsTriageResults, &triageResults); err != nil {
			return fmt.Errorf("loading triage results: %w", err)
		}

		ghToken := os.Getenv(githubTokenEnvVar)
		if ghToken == "" {
			return fmt.Errorf("%s environment variable is required", githubTokenEnvVar)
		}
		gh := ghclient.NewClient(ghToken)

		var tracker *state.Tracker
		if stateFile != "" {
			tracker = state.NewTracker(stateFile)
		}

		mcpClient := qase.NewMCPClient(mcpURL)
		qaseToken := os.Getenv(qaseApiTokenEnvVar)
		qaseClient := qase.NewClient(qaseToken)

		result := types.DefectActions{}

		// Combine product and test defects
		allDefects := make([]types.TriageEntry, 0, len(triageResults.ProductDefects)+len(triageResults.TestDefects))
		allDefects = append(allDefects, triageResults.ProductDefects...)
		allDefects = append(allDefects, triageResults.TestDefects...)

		for _, defect := range allDefects {
			// Determine target repo
			targetRepo := defectsProductRepo
			if defect.Classification == triage.ClassTestDefect {
				targetRepo = defectsTestsRepo
			}

			owner, repo, err := ghclient.ParseRepo(targetRepo)
			if err != nil {
				logrus.Errorf("Invalid repo %s: %v", targetRepo, err)
				result.Escalated = append(result.Escalated, types.EscalatedDefect{
					TestName: defect.TestName,
					Reason:   fmt.Sprintf("invalid repo: %v", err),
				})
				continue
			}

			// Create GitHub issue
			if defectsAutoCreateIssue && !dryRun {
				title := fmt.Sprintf("[Agentic QA] %s: %s", defect.Classification, defect.TestName)
				body := buildIssueBody(defect, defectsPRNumber)
				labels := []string{activePipelineEnv().AgenticQALabel, string(defect.Classification)}
				if defect.RecommendedSeverity != "" {
					labels = append(labels, defectSeverityPrefix+string(defect.RecommendedSeverity))
				}

				if localTest {
					title = localTestPrefix + title
					body = "> ⚠️ **LOCAL TEST Artifact** — created by `--local-test` mode. Safe to delete.\n\n" + body
					logrus.Infof("Local-test mode: prefixing issue title with %q for %s", localTestPrefix, defect.TestName)
				}

				issueURL, err := gh.CreateIssue(ctx, owner, repo, title, body, labels)
				if err != nil {
					logrus.Errorf("Failed to create issue for %s: %v", defect.TestName, err)
					result.Escalated = append(result.Escalated, types.EscalatedDefect{
						TestName: defect.TestName,
						Reason:   fmt.Sprintf("failed to create issue: %v", err),
					})
					continue
				}

				result.IssuesCreated = append(result.IssuesCreated, types.CreatedIssue{
					URL:        issueURL,
					Title:      title,
					DefectType: string(defect.Classification),
				})

				// Track in state
				if tracker != nil {
					issueNum := extractIssueNumber(issueURL)
					if issueNum > 0 {
						if err := tracker.AddGithubIssue(targetRepo, issueNum); err != nil {
							logrus.Warnf("Failed to track issue in state: %v", err)
						}
					}
				}

				// Create Qase defect
				severity := string(defect.RecommendedSeverity)
				if severity == "" {
					severity = defectNormalSeverity // Qase API default severity
				}

				if mcpClient.IsConfigured() {
					defectID, err := mcpClient.CreateDefect(ctx, qaseProject, title, severity, defect.Evidence)
					if err != nil {
						logrus.Warnf("MCP create defect failed: %v", err)
					} else if tracker != nil {
						if err := tracker.AddQaseDefect(qaseProject, defectID); err != nil {
							logrus.Warnf("Failed to track Qase defect: %v", err)
						}
					}
				} else if qaseToken != "" {
					defectID, err := qaseClient.CreateDefect(ctx, qaseProject, title, severity, defect.Evidence)
					if err != nil {
						logrus.Warnf("REST create defect failed: %v", err)
					} else if tracker != nil {
						if err := tracker.AddQaseDefect(qaseProject, defectID); err != nil {
							logrus.Warnf("Failed to track Qase defect: %v", err)
						}
					}
				}

				// Copilot or Claude fix generation
				if defectsUseCopilot && !localTest {
					issueNum := extractIssueNumber(issueURL)
					if issueNum > 0 {
						if err := gh.AssignCopilot(ctx, owner, repo, issueNum, ghToken, activePipelineEnv().CopilotUsername); err != nil {
							logrus.Warnf("Failed to assign Copilot to %s: %v", issueURL, err)
						} else {
							result.CopilotAssignments = append(result.CopilotAssignments, types.CopilotAssignment{
								IssueURL: issueURL,
								Repo:     targetRepo,
							})
						}
					}
				}
			} else if dryRun {
				logrus.Infof("Dry-run: would create issue for %s in %s/%s", defect.TestName, owner, repo)
			} else {
				result.Escalated = append(result.Escalated, types.EscalatedDefect{
					TestName: defect.TestName,
					Reason:   "auto-create-issues is disabled",
				})
			}
		}

		if err := saveJSON(defectsOutputFile, result); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		logrus.Infof("Defect handling complete: %d issues, %d PRs, %d copilot, %d escalated → %s",
			len(result.IssuesCreated), len(result.PRsOpened), len(result.CopilotAssignments), len(result.Escalated), defectsOutputFile)
		return nil
	},
}

func buildIssueBody(entry types.TriageEntry, prNumber int) string {
	var sb strings.Builder
	sb.WriteString("## Agentic QA Failure Report\n\n")
	sb.WriteString(fmt.Sprintf("**Test:** `%s`\n", entry.TestName))
	sb.WriteString(fmt.Sprintf("**Package:** `%s`\n", entry.Package))
	sb.WriteString(fmt.Sprintf("**Classification:** %s\n", entry.Classification))
	sb.WriteString(fmt.Sprintf("**Confidence:** %s\n", entry.Confidence))
	if entry.RecommendedSeverity != "" {
		sb.WriteString(fmt.Sprintf("**Severity:** %s\n", entry.RecommendedSeverity))
	}
	if prNumber > 0 {
		sb.WriteString(fmt.Sprintf("**Source PR:** #%d\n", prNumber))
	}
	sb.WriteString(fmt.Sprintf("\n### Evidence\n\n%s\n", entry.Evidence))
	if entry.Error != "" {
		sb.WriteString(fmt.Sprintf("\n### Error\n\n```\n%s\n```\n", entry.Error))
	}
	if entry.RecommendedAction != "" {
		sb.WriteString(fmt.Sprintf("\n### Recommended Action\n\n%s\n", entry.RecommendedAction))
	}
	return sb.String()
}

// extractIssueNumber extracts the issue number from a GitHub issue URL.
func extractIssueNumber(url string) int {
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	if len(parts) == 0 {
		return 0
	}
	var num int
	fmt.Sscanf(parts[len(parts)-1], "%d", &num)
	return num
}
