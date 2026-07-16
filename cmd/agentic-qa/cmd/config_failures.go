package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/jenkins"
	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	cfMaxRerunsFlag = "max-reruns"
)

var (
	cfTriageResults  string
	cfPipelineConfig string
	cfTriggerMapping string
	cfMaxReruns      int
	cfTestsRepo      string
	cfAutoCreatePRs  bool
	cfOutputFile     string
	cfJenkinsURL     string
)

func init() {
	f := configFailuresCmd.Flags()
	f.StringVar(&cfTriageResults, triageResultsFlag, "", "Path to triage_results.json (required)")
	f.StringVar(&cfPipelineConfig, pipelineConfigFlag, "", "Path to pipeline config")
	f.StringVar(&cfTriggerMapping, triggerMappingFlag, "", "Path to jenkins_trigger_mapping.json")
	f.IntVar(&cfMaxReruns, cfMaxRerunsFlag, defaultMaxReruns, "Maximum number of reruns per test")
	f.StringVar(&cfTestsRepo, testsRepoFlag, defaultTestsRepo, "Tests repository (owner/repo)")
	f.BoolVar(&cfAutoCreatePRs, autoCreatePRsFlag, false, "Automatically create GitHub PRs for guards")
	f.StringVar(&cfOutputFile, outputFileFlag, "", "Path to write config_actions.json (required)")
	f.StringVar(&cfJenkinsURL, jenkinsURLFlag, "", "Jenkins server URL")

	_ = configFailuresCmd.MarkFlagRequired(triageResultsFlag)
	_ = configFailuresCmd.MarkFlagRequired(outputFileFlag)

	rootCmd.AddCommand(configFailuresCmd)
}

var configFailuresCmd = &cobra.Command{
	Use:   "config-failures",
	Short: "Handle configuration failures",
	Long:  `Analyzes configuration/environment failures and either reruns tests or generates guard code.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		var triageResults types.TriageResults
		if err := loadJSON(cfTriageResults, &triageResults); err != nil {
			return fmt.Errorf("loading triage results: %w", err)
		}

		if len(triageResults.ConfigIssues) == 0 {
			logrus.Info("No configuration issues to handle")
			result := types.ConfigActions{}
			return saveJSON(cfOutputFile, result)
		}

		haikuClient, err := newLLMClient(ctx, haikuModel)
		if err != nil {
			return fmt.Errorf("creating haiku LLM client: %w", err)
		}

		jenkinsURL := cfJenkinsURL
		if jenkinsURL == "" {
			jenkinsURL = os.Getenv(jenkinsURLEnvVar)
		}

		var triggerMapping map[string]any
		if cfTriggerMapping != "" {
			if err := loadJSON(cfTriggerMapping, &triggerMapping); err != nil {
				logrus.Warnf("Could not load trigger mapping: %v", err)
			}
		}

		result := types.ConfigActions{}

		for _, issue := range triageResults.ConfigIssues {
			// Use Haiku to choose rerun or guard generation.
			decisionPrompt := `You are a test infrastructure expert. Given a configuration/environment test failure,
decide whether to:
1. "rerun" - the failure is transient and a rerun should fix it
2. "guard" - the failure needs a code guard to prevent recurrence

Respond with JSON: {"action": "rerun|guard", "reasoning": "string"}`

			userMsg := fmt.Sprintf("Test: %s\nError: %s\nPattern: %s\nEvidence: %s",
				issue.TestName, issue.Error, issue.PatternMatched, issue.Evidence)

			var decision struct {
				Action    string `json:"action"`
				Reasoning string `json:"reasoning"`
			}

			if err := haikuClient.CompleteJSON(ctx, decisionPrompt, userMsg, llmMaxTokensDecision, &decision); err != nil {
				logrus.Warnf("LLM decision failed for %s: %v, defaulting to rerun", issue.TestName, err)
				decision.Action = types.CFActionRerun
			}

			switch decision.Action {
			case types.CFActionRerun:
				if jenkinsURL == "" || dryRun || localTest {
					if localTest {
						logrus.Infof("Local-test mode: skipping Jenkins rerun for %s", issue.TestName)
					} else {
						logrus.Infof("Would rerun %s (dry-run or no Jenkins URL)", issue.TestName)
					}
					result.Reruns = append(result.Reruns, types.RerunEntry{
						TestName:      issue.TestName,
						OriginalError: issue.Error,
						Attempt:       1,
					})
					continue
				}

				jenkinsUser := activeJenkinsUser()
				jenkinsToken := os.Getenv(jenkinsTokenEnvVar)
				jClient := jenkins.NewClient(jenkinsURL, jenkinsUser, jenkinsToken)

				folder, jobName := splitConfigJobName(issue.TestName, triggerMapping)
				params := map[string]string{}

				logrus.Infof("Rerunning %s/%s", folder, jobName)
				queueID, err := jClient.TriggerBuild(ctx, folder, jobName, params)
				if err != nil {
					logrus.Errorf("Failed to rerun %s: %v", issue.TestName, err)
					result.Reruns = append(result.Reruns, types.RerunEntry{
						TestName:      issue.TestName,
						OriginalError: issue.Error,
						Attempt:       1,
					})
				} else {
					result.Reruns = append(result.Reruns, types.RerunEntry{
						TestName:      issue.TestName,
						JobName:       fmt.Sprintf("%s/%s", folder, jobName),
						OriginalError: issue.Error,
						RerunQueueID:  &queueID,
						Attempt:       1,
					})
				}

			case types.CFActionGuard:
				logrus.Infof("Generating guard for %s", issue.TestName)

				sonnetClient, err := newLLMClient(ctx, sonnetModel)
				if err != nil {
					logrus.Errorf("Failed to create sonnet client for guard generation: %v", err)
					continue
				}

				guardPrompt := `You are a Go test engineer. Generate guard code to prevent a configuration/environment
test failure from recurring. The guard should check preconditions before the test runs.

Respond with JSON:
{
  "guard_type": "skip_condition|retry_wrapper|setup_check",
  "file_modified": "string (test file path)",
  "code": "string (Go code to add)",
  "description": "string"
}`

				guardMsg := fmt.Sprintf("Test: %s\nPackage: %s\nError: %s\nPattern: %s",
					issue.TestName, issue.Package, issue.Error, issue.PatternMatched)

				var guard struct {
					GuardType    string `json:"guard_type"`
					FileModified string `json:"file_modified"`
					Code         string `json:"code"`
					Description  string `json:"description"`
				}

				if err := sonnetClient.CompleteJSON(ctx, guardPrompt, guardMsg, llmMaxTokensGuard, &guard); err != nil {
					logrus.Errorf("Guard generation failed for %s: %v", issue.TestName, err)
					continue
				}

				result.Guards = append(result.Guards, types.GuardEntry{
					TestName:     issue.TestName,
					GuardType:    guard.GuardType,
					FileModified: guard.FileModified,
					Description:  guard.Description,
				})
			}
		}

		if err := saveJSON(cfOutputFile, result); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		logrus.Infof("Config failures handled: %d reruns, %d guards, %d PRs → %s",
			len(result.Reruns), len(result.Guards), len(result.PRsOpened), cfOutputFile)
		return nil
	},
}

// splitConfigJobName resolves a test's Jenkins folder and job.
func splitConfigJobName(testName string, mapping map[string]any) (folder, jobName string) {
	if mapping != nil {
		if jobConfig, ok := mapping[testName].(map[string]any); ok {
			f, _ := jobConfig["folder"].(string)
			j, _ := jobConfig["job_name"].(string)
			if j != "" {
				return f, j
			}
		}
	}
	parts := strings.SplitN(testName, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", testName
}
