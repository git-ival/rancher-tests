package cmd

import (
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/triage"
	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	analyzeCompletedJobsFlag   = "completed-jobs"
	analyzeTriageFrameworkFlag = "triage-framework"
	analyzeFeatureMappingFlag  = "feature-mapping"
)

var (
	analyzeCompletedJobs     string
	analyzeTriageFramework   string
	analyzeFeatureMapping    string
	analyzeOutputFile        string
	analyzeAdditionalContext string
)

func init() {
	f := analyzeCmd.Flags()
	f.StringVar(&analyzeCompletedJobs, analyzeCompletedJobsFlag, "", "Path to completed_jobs.json (required)")
	f.StringVar(&analyzeTriageFramework, analyzeTriageFrameworkFlag, "", "Path to triage_framework.json")
	f.StringVar(&analyzeFeatureMapping, analyzeFeatureMappingFlag, "", "Path to feature_test_mapping.json")
	f.StringVar(&analyzeOutputFile, outputFileFlag, "", "Path to write triage_results.json (required)")
	f.StringVar(&analyzeAdditionalContext, additionalContextFlag, "", "Path to additional context file")

	_ = analyzeCmd.MarkFlagRequired(analyzeCompletedJobsFlag)
	_ = analyzeCmd.MarkFlagRequired(outputFileFlag)

	rootCmd.AddCommand(analyzeCmd)
}

var analyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "Triage test failures",
	Long:  `Classifies test failures using pattern matching and LLM analysis.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		var completedJobs types.CompletedJobs
		if err := loadJSON(analyzeCompletedJobs, &completedJobs); err != nil {
			return fmt.Errorf("loading completed jobs: %w", err)
		}

		var frameworkData string
		if analyzeTriageFramework != "" {
			data, err := os.ReadFile(analyzeTriageFramework)
			if err != nil {
				logrus.Warnf("Could not load triage framework: %v", err)
			} else {
				frameworkData = string(data)
			}
		}

		var additionalContext string
		if analyzeAdditionalContext != "" {
			data, err := os.ReadFile(analyzeAdditionalContext)
			if err != nil {
				logrus.Warnf("Could not load additional context: %v", err)
			} else {
				additionalContext = string(data)
			}
		}

		allPatterns := make([]triage.PatternRule, 0,
			len(triage.EnvPatterns)+len(triage.TestDefectPatterns)+len(triage.ProductDefectPatterns))
		allPatterns = append(allPatterns, triage.EnvPatterns...)
		allPatterns = append(allPatterns, triage.TestDefectPatterns...)
		allPatterns = append(allPatterns, triage.ProductDefectPatterns...)

		result := types.TriageResults{
			QaseRunID:  completedJobs.QaseRunID,
			TotalTests: len(completedJobs.Completed) + len(completedJobs.Failed),
		}

		for _, job := range completedJobs.Completed {
			result.Passed = append(result.Passed, types.TriageEntry{
				TestName:       job.JobName,
				Classification: types.ClassPassed,
			})
		}

		var llmNeeded []types.CompletedJob
		for _, job := range completedJobs.Failed {
			entry := classifyByPattern(job, allPatterns)
			if entry != nil {
				switch entry.Classification {
				case types.ClassProductDefect:
					result.ProductDefects = append(result.ProductDefects, *entry)
				case types.ClassTestDefect:
					result.TestDefects = append(result.TestDefects, *entry)
				case types.ClassConfigEnvironment:
					result.ConfigIssues = append(result.ConfigIssues, *entry)
				}
			} else {
				llmNeeded = append(llmNeeded, job)
			}
		}

		if len(llmNeeded) > 0 {
			llmClient, err := newLLMClient(ctx, sonnetModel)
			if err != nil {
				logrus.Errorf("Failed to create LLM client for triage: %v", err)
				for _, job := range llmNeeded {
					result.Unknown = append(result.Unknown, types.TriageEntry{
						TestName:       job.JobName,
						Classification: types.ClassUnknown,
						Confidence:     types.ConfidenceLow,
						Evidence:       "LLM client creation failed",
					})
				}
			} else {
				systemPrompt := buildTriageSystemPrompt(frameworkData, additionalContext)

				for _, job := range llmNeeded {
					logrus.Infof("Using LLM to classify failure: %s", job.JobName)

					userMsg := fmt.Sprintf("Test: %s\nStatus: %s\nLog URL: %s\nDuration: %.1f min",
						job.JobName, job.Status, job.LogURL, job.DurationMinutes)

					var triageEntry types.TriageEntry
					if err := llmClient.CompleteJSON(ctx, systemPrompt, userMsg, llmMaxTokensTriage, &triageEntry); err != nil {
						logrus.Warnf("LLM triage failed for %s: %v", job.JobName, err)
						triageEntry = types.TriageEntry{
							TestName:       job.JobName,
							Classification: types.ClassUnknown,
							Confidence:     types.ConfidenceLow,
							Evidence:       fmt.Sprintf("LLM triage failed: %v", err),
						}
					}
					triageEntry.TestName = job.JobName

					switch triageEntry.Classification {
					case types.ClassProductDefect:
						result.ProductDefects = append(result.ProductDefects, triageEntry)
					case types.ClassTestDefect:
						result.TestDefects = append(result.TestDefects, triageEntry)
					case types.ClassConfigEnvironment:
						result.ConfigIssues = append(result.ConfigIssues, triageEntry)
					default:
						result.Unknown = append(result.Unknown, triageEntry)
					}
				}
			}
		}

		if err := saveJSON(analyzeOutputFile, result); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		logrus.Infof("Triage complete: %d passed, %d product defects, %d test defects, %d config issues, %d unknown → %s",
			len(result.Passed), len(result.ProductDefects), len(result.TestDefects), len(result.ConfigIssues), len(result.Unknown), analyzeOutputFile)
		return nil
	},
}

// classifyByPattern returns the matching classification, or nil.
func classifyByPattern(job types.CompletedJob, patterns []triage.PatternRule) *types.TriageEntry {
	// TODO: Fetch logs; only the job name and log URL are matched.
	searchText := job.JobName + " " + job.Status + " " + job.LogURL
	for _, p := range patterns {
		if p.Pattern.MatchString(searchText) {
			return &types.TriageEntry{
				TestName:       job.JobName,
				Classification: types.TriageClassification(p.Category),
				Confidence:     types.TriageConfidence(p.Confidence),
				PatternMatched: p.Description,
			}
		}
	}
	return nil
}

func buildTriageSystemPrompt(framework, additionalContext string) string {
	env := activePipelineEnv()
	prompt := fmt.Sprintf(`You are a test failure triage expert for the %s project.
Classify the test failure into one of these categories:
- "product_defect": a bug in the product
- "test_defect": a bug in the test code itself
- "config_environment": an infrastructure or configuration issue

Respond with a JSON object:
{
  "test_name": "string",
  "classification": "product_defect|test_defect|config_environment",
  "confidence": "high|medium|low",
  "evidence": "string explaining the classification",
  "recommended_severity": "blocker|critical|major|normal|minor|trivial",
  "recommended_action": "string"
}`, env.ProjectDisplayName)

	if framework != "" {
		prompt += fmt.Sprintf("\n\nTriage framework:\n%s", framework)
	}
	if additionalContext != "" {
		prompt += fmt.Sprintf("\n\nAdditional context:\n%s", additionalContext)
	}
	return prompt
}
