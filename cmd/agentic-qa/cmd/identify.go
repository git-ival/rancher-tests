package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	ghclient "github.com/rancher/tests/internal/agenticqa/github"
	"github.com/rancher/tests/internal/agenticqa/types"
)

var (
	identifyPRNumber              int
	identifyRepo                  string
	identifyMappingFile           string
	identifyOutputFile            string
	identifyAdditionalContextFile string
)

func init() {
	f := identifyCmd.Flags()
	f.IntVar(&identifyPRNumber, "pr-number", 0, "Pull request number (required)")
	f.StringVar(&identifyRepo, "repo", "rancher/rancher", "Repository (owner/repo)")
	f.StringVar(&identifyMappingFile, "mapping-file", "", "Path to feature_test_mapping.json (required)")
	f.StringVar(&identifyOutputFile, "output-file", "", "Path to write identified_tests.json (required)")
	f.StringVar(&identifyAdditionalContextFile, "additional-context-file", "", "Path to additional context file (optional)")

	_ = identifyCmd.MarkFlagRequired("pr-number")
	_ = identifyCmd.MarkFlagRequired("mapping-file")
	_ = identifyCmd.MarkFlagRequired("output-file")

	rootCmd.AddCommand(identifyCmd)
}

var identifyCmd = &cobra.Command{
	Use:   "identify",
	Short: "Identify tests relevant to a PR",
	Long:  `Analyzes a PR diff against a feature-test mapping to identify relevant tests using an LLM.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		ghToken := os.Getenv("GITHUB_TOKEN")
		if ghToken == "" {
			return fmt.Errorf("GITHUB_TOKEN environment variable is required")
		}

		gh := ghclient.NewClient(ghToken)

		owner, repo, err := ghclient.ParseRepo(identifyRepo)
		if err != nil {
			return fmt.Errorf("parsing repo: %w", err)
		}

		logrus.Infof("Fetching PR #%d from %s/%s", identifyPRNumber, owner, repo)

		prInfo, err := gh.GetPRInfo(ctx, owner, repo, identifyPRNumber)
		if err != nil {
			return fmt.Errorf("getting PR info: %w", err)
		}

		diff, err := gh.GetPRDiff(ctx, owner, repo, identifyPRNumber)
		if err != nil {
			return fmt.Errorf("getting PR diff: %w", err)
		}

		files, err := gh.GetPRFiles(ctx, owner, repo, identifyPRNumber)
		if err != nil {
			return fmt.Errorf("getting PR files: %w", err)
		}

		mappingData, err := os.ReadFile(identifyMappingFile)
		if err != nil {
			return fmt.Errorf("reading mapping file: %w", err)
		}

		systemPrompt := buildIdentifySystemPrompt(string(mappingData))

		var additionalContext string
		if identifyAdditionalContextFile != "" {
			data, err := os.ReadFile(identifyAdditionalContextFile)
			if err != nil {
				return fmt.Errorf("reading additional context file: %w", err)
			}
			additionalContext = string(data)
		}

		userMessage := buildIdentifyUserMessage(prInfo, diff, files, additionalContext)

		llmClient, err := newLLMClient(ctx, haikuModel)
		if err != nil {
			return fmt.Errorf("creating LLM client: %w", err)
		}

		logrus.Info("Calling LLM to identify relevant tests...")

		var result types.IdentifiedTests
		if err := llmClient.CompleteJSON(ctx, systemPrompt, userMessage, 4096, &result); err != nil {
			return fmt.Errorf("LLM identification failed: %w", err)
		}

		result.PRNumber = identifyPRNumber
		result.PRURL = prInfo.URL
		result.PRTitle = prInfo.Title
		result.ChangedFiles = files

		if err := saveJSON(identifyOutputFile, result); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		logrus.Infof("Identified %d tests, %d recommended tags, %d recommended jobs → %s",
			len(result.Tests), len(result.RecommendedTags), len(result.RecommendedJobs), identifyOutputFile)
		return nil
	},
}

func buildIdentifySystemPrompt(mappingJSON string) string {
	return fmt.Sprintf(`You are a test selection expert for the Rancher project.
Given a PR diff and a feature-to-test mapping, identify which tests should be run.

The feature_test_mapping.json maps feature areas to test files, suites, and tags:
%s

Respond with a JSON object matching this schema:
{
  "feature_areas": ["string"],
  "tests": [{"file": "string", "suite": "string", "functions": ["string"], "build_tags": ["string"], "qase_projects": ["string"], "relevance_score": 0.0, "reasoning": "string"}],
  "recommended_tags": ["string"],
  "recommended_jobs": ["string"],
  "confidence": "high|medium|low"
}`, mappingJSON)
}

func buildIdentifyUserMessage(prInfo *ghclient.PRInfo, diff string, files []string, additionalContext string) string {
	filesJSON, _ := json.Marshal(files)
	msg := fmt.Sprintf(`PR #%d: %s
Author: %s
Branch: %s → %s
URL: %s

Changed files:
%s

Diff:
%s`, prInfo.Number, prInfo.Title, prInfo.Author, prInfo.Branch, prInfo.Base, prInfo.URL,
		string(filesJSON), truncate(diff, 50000))

	if additionalContext != "" {
		msg += fmt.Sprintf("\n\nAdditional context:\n%s", additionalContext)
	}
	return msg
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... (truncated)"
}


