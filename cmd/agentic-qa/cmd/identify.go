package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

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

const (
	identifyMappingFileFlag = "mapping-file"
)

func init() {
	f := identifyCmd.Flags()
	f.IntVar(&identifyPRNumber, prNumberFlag, 0, "Pull request number (required)")
	f.StringVar(&identifyRepo, repoFlag, defaultProductRepo, "Repository (owner/repo)")
	f.StringVar(&identifyMappingFile, identifyMappingFileFlag, "", "Path to feature_test_mapping.json (required)")
	f.StringVar(&identifyOutputFile, outputFileFlag, "", "Path to write identified_tests.json (required)")
	f.StringVar(&identifyAdditionalContextFile, additionalContextFlag, "", "Path to additional context file (optional)")

	_ = identifyCmd.MarkFlagRequired(prNumberFlag)
	_ = identifyCmd.MarkFlagRequired(identifyMappingFileFlag)
	_ = identifyCmd.MarkFlagRequired(outputFileFlag)

	rootCmd.AddCommand(identifyCmd)
}

// consolidateQaseProjects assigns each test to exactly one Qase project using
// a frequency-greedy algorithm: the project that appears most often across the
// identified test set wins. On frequency ties, lexicographic order is the
// tiebreaker.
//
// Only projects that actually appear in at least one test's QaseCasesByProject
// (or QaseProjects as fallback) are considered. Tests with no project
// association are excluded from consolidation.
//
// When no --qase-project flag is set on trigger, ALL resulting projects will
// each get a Qase run with their assigned tests triggered.
func consolidateQaseProjects(
	tests []types.TestEntry,
) (projects []string, byProject map[string][]int) {
	// 1. Frequency pass: count how many tests list each project.
	freq := map[string]int{}
	for _, t := range tests {
		if len(t.QaseCasesByProject) > 0 {
			for p := range t.QaseCasesByProject {
				freq[p]++
			}
		} else {
			for _, p := range t.QaseProjects {
				freq[p]++
			}
		}
	}

	// 2. Build dynamic priority: sorted by frequency descending, then lexicographic.
	var sortedProjects []string
	for p := range freq {
		sortedProjects = append(sortedProjects, p)
	}
	sort.Slice(sortedProjects, func(i, j int) bool {
		if freq[sortedProjects[i]] != freq[sortedProjects[j]] {
			return freq[sortedProjects[i]] > freq[sortedProjects[j]]
		}
		return sortedProjects[i] < sortedProjects[j]
	})

	priority := make(map[string]int)
	for idx, p := range sortedProjects {
		priority[p] = idx
	}

	// 3. Assignment pass: pick the best (highest-frequency) project for each test.
	byProject = map[string][]int{}
	for i, t := range tests {
		var candidates []string
		if len(t.QaseCasesByProject) > 0 {
			for p := range t.QaseCasesByProject {
				candidates = append(candidates, p)
			}
		} else {
			candidates = t.QaseProjects
		}
		if len(candidates) == 0 {
			continue
		}

		best := candidates[0]
		for _, p := range candidates[1:] {
			if freq[p] > freq[best] {
				best = p
			} else if freq[p] == freq[best] && priority[p] < priority[best] {
				best = p
			}
		}
		byProject[best] = append(byProject[best], i)
	}

	// 4. Collect sorted project list.
	for p := range byProject {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	return projects, byProject
}

var identifyCmd = &cobra.Command{
	Use:   "identify",
	Short: "Identify tests relevant to a PR",
	Long: `Analyzes a PR diff against a feature-test mapping to identify relevant tests
using an LLM. Enriches identified tests only from the generated mapping file.
If an identified test has no mapped qase_cases, identify fails with an error.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		ghToken := os.Getenv(githubTokenEnvVar)
		if ghToken == "" {
			return fmt.Errorf("%s environment variable is required", githubTokenEnvVar)
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

		// Load and parse the pre-generated feature test mapping.
		mappingData, err := os.ReadFile(identifyMappingFile)
		if err != nil {
			return fmt.Errorf("reading mapping file: %w", err)
		}

		var mapping types.FeatureTestMapping
		if err := json.Unmarshal(mappingData, &mapping); err != nil {
			return fmt.Errorf("parsing mapping file: %w", err)
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

		// Enrich identified tests using only qase_cases from the pre-generated mapping.
		if err := enrichQaseCasesFromMapping(&result, &mapping); err != nil {
			return fmt.Errorf("enriching Qase cases from mapping: %w", err)
		}

		result.QaseProjects, result.TestsByProject = consolidateQaseProjects(result.Tests)
		logrus.Infof("Consolidated %d tests into %d Qase project(s): %v",
			len(result.Tests), len(result.QaseProjects), result.QaseProjects)

		if err := saveJSON(identifyOutputFile, result); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		logrus.Infof("Identified %d tests, %d recommended tags, %d recommended jobs → %s",
			len(result.Tests), len(result.RecommendedTags), len(result.RecommendedJobs), identifyOutputFile)
		return nil
	},
}

func buildIdentifySystemPrompt(mappingJSON string) string {
	env := activePipelineEnv()
	return fmt.Sprintf(`You are a test selection expert for the %s project.
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
}`, env.ProjectDisplayName, mappingJSON)
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

// enrichQaseCasesFromMapping enriches identified tests using only qase_cases
// from the pre-generated feature_test_mapping.json. No live Qase API fallback
// or validation is performed here.
//
// This is intentionally strict: every identified test must have mapped case
// IDs, otherwise identify returns an error so trigger can remain mapping-only.
func enrichQaseCasesFromMapping(result *types.IdentifiedTests, mapping *types.FeatureTestMapping) error {
	// Build an index: file path → TestFile from the mapping.
	fileIndex := buildFileIndex(mapping)

	missing := 0

	for i := range result.Tests {
		t := &result.Tests[i]
		tf, found := fileIndex[t.File]
		if !found {
			logrus.Errorf("Test file %q not found in feature_test_mapping.json", t.File)
			missing++
			continue
		}

		if len(tf.QaseCases) == 0 {
			logrus.Errorf("Test file %q has no qase_cases in the mapping", t.File)
			missing++
			continue
		}

		// Enrich the TestEntry with projects and case IDs from the mapping only.
		t.QaseProjects = append([]string(nil), tf.QaseProjects...)
		sort.Strings(t.QaseProjects)
		t.QaseCaseIDs = nil
		t.QaseCasesByProject = nil
		for _, c := range tf.QaseCases {
			if c.ID > 0 {
				t.QaseCaseIDs = append(t.QaseCaseIDs, c.ID)
			}
		}
		if len(t.QaseCaseIDs) == 0 {
			logrus.Errorf("Test file %q has qase_cases entries, but none include a valid ID", t.File)
			missing++
		}
	}

	if missing > 0 {
		return fmt.Errorf("%d identified test(s) are missing deterministic qase_cases in %s; regenerate mapping with generate-feature-map", missing, identifyMappingFile)
	}

	logrus.Infof("Enriched %d identified tests with mapping-provided Qase case IDs", len(result.Tests))
	return nil
}

// buildFileIndex creates a map from file path → TestFile for fast lookup.
func buildFileIndex(mapping *types.FeatureTestMapping) map[string]types.TestFile {
	idx := make(map[string]types.TestFile)
	for _, area := range mapping.FeatureAreas {
		for _, tf := range area.TestFiles {
			idx[tf.Path] = tf
		}
	}
	return idx
}
