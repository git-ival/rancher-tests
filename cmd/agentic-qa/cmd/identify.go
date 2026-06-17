package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	ghclient "github.com/rancher/tests/internal/agenticqa/github"
	"github.com/rancher/tests/internal/agenticqa/qase"
	"github.com/rancher/tests/internal/agenticqa/types"
)

var (
	identifyPRNumber              int
	identifyRepo                  string
	identifyMappingFile           string
	identifyOutputFile            string
	identifyAdditionalContextFile string
	identifyQaseProjects          []string
)

const (
	identifyPRNumberFlag              = "pr-number"
	identifyRepoFlag                  = "repo"
	identifyMappingFileFlag           = "mapping-file"
	identifyOutputFileFlag            = "output-file"
	identifyAdditionalContextFileFlag = "additional-context-file"
	identifyQaseProjectsFlag          = "qase-projects"
)

func init() {
	f := identifyCmd.Flags()
	f.IntVar(&identifyPRNumber, identifyPRNumberFlag, 0, "Pull request number (required)")
	f.StringVar(&identifyRepo, identifyRepoFlag, "rancher/rancher", "Repository (owner/repo)")
	f.StringVar(&identifyMappingFile, identifyMappingFileFlag, "", "Path to feature_test_mapping.json (required)")
	f.StringVar(&identifyOutputFile, identifyOutputFileFlag, "", "Path to write identified_tests.json (required)")
	f.StringVar(&identifyAdditionalContextFile, identifyAdditionalContextFileFlag, "", "Path to additional context file (optional)")
	f.StringSliceVar(&identifyQaseProjects, identifyQaseProjectsFlag, []string{"RANCHERINT", "RRT", "RM", "K3SRKE2"},
		"Qase project codes to scan for title-based fallback matching (comma-separated)")

	_ = identifyCmd.MarkFlagRequired(identifyPRNumberFlag)
	_ = identifyCmd.MarkFlagRequired(identifyMappingFileFlag)
	_ = identifyCmd.MarkFlagRequired(identifyOutputFileFlag)

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
using an LLM. Validates identified test cases against Qase to confirm they exist.
Tests without qase_cases in the mapping are logged at WARN level and processing
continues with the remaining tests.`,
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

		// Enrich identified tests with qase_cases from the pre-generated mapping
		// and validate them against the Qase API.
		if err := enrichAndValidateQaseCases(ctx, &result, &mapping, identifyQaseProjects); err != nil {
			return fmt.Errorf("validating Qase cases: %w", err)
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

// enrichAndValidateQaseCases does two things:
//  1. For each identified test, looks up its qase_cases from the pre-generated
//     feature_test_mapping.json (keyed by file path). Populates QaseCaseIDs and
//     QaseProjects on the TestEntry.
//  2. Validates the case IDs actually exist in Qase by querying the API for
//     each relevant project. Cases that don't exist are logged and skipped.
//
// Tests with no qase_cases in the mapping are logged at WARN level and
// processing continues — they will not have QaseCaseIDs populated but will
// still appear in the identified tests output for Jenkins triggering.
func enrichAndValidateQaseCases(ctx context.Context, result *types.IdentifiedTests, mapping *types.FeatureTestMapping, fallbackProjects []string) error {
	qaseToken := os.Getenv("QASE_API_TOKEN")
	if qaseToken == "" {
		return fmt.Errorf("QASE_API_TOKEN environment variable is required")
	}

	// Build an index: file path → TestFile from the mapping.
	fileIndex := buildFileIndex(mapping)

	// First pass: enrich tests with qase_cases from the mapping.
	projectSet := map[string]struct{}{}
	testsWithCases := 0
	testsWithoutCases := 0

	for i := range result.Tests {
		t := &result.Tests[i]
		tf, found := fileIndex[t.File]
		if !found {
			logrus.Warnf("Test file %q not found in feature_test_mapping.json — no Qase cases mapped", t.File)
			testsWithoutCases++
			continue
		}

		if len(tf.QaseCases) == 0 {
			logrus.Warnf("Test file %q has no qase_cases in the mapping — will proceed without Qase tracking", t.File)
			testsWithoutCases++
			continue
		}

		// Enrich the TestEntry with projects and case IDs from the mapping.
		if len(t.QaseProjects) == 0 {
			t.QaseProjects = tf.QaseProjects
		}
		for _, c := range tf.QaseCases {
			if c.ID > 0 {
				t.QaseCaseIDs = append(t.QaseCaseIDs, c.ID)
			}
		}
		for _, p := range tf.QaseProjects {
			projectSet[p] = struct{}{}
		}
		testsWithCases++
	}

	logrus.Infof("Enriched %d tests with Qase cases; %d tests have no Qase mapping (WARN)",
		testsWithCases, testsWithoutCases)

	qaseClient := qase.NewClient(qaseToken)

	// Title fallback: for tests that still have no QaseCaseIDs, try matching
	// their function names against Qase case titles across all fallback projects.
	if testsWithoutCases > 0 && len(fallbackProjects) > 0 {
		titleMaps := map[string]map[string]int{} // lazy-loaded per project
		fallbackResolved := 0

		for i := range result.Tests {
			t := &result.Tests[i]
			if len(t.QaseCaseIDs) > 0 || len(t.Functions) == 0 {
				continue
			}

			candidates := buildCandidateNames(t)
			for _, name := range candidates {
				for _, project := range fallbackProjects {
					tMap, ok := titleMaps[project]
					if !ok {
						logrus.Infof("Fetching Qase title map for project %s (title fallback)...", project)
						m, err := qaseClient.GetTitleMap(ctx, project)
						if err != nil {
							logrus.Warnf("Failed to fetch title map for project %s: %v", project, err)
							titleMaps[project] = map[string]int{}
							continue
						}
						titleMaps[project] = m
						tMap = m
						logrus.Infof("  %s: %d case titles indexed", project, len(m))
					}
					if caseID, found := tMap[name]; found {
						t.QaseCaseIDs = append(t.QaseCaseIDs, caseID)
						t.QaseProjects = appendUnique(t.QaseProjects, project)
						projectSet[project] = struct{}{}
						fallbackResolved++
						break // first project match wins for this name
					}
				}
			}
			if len(t.QaseCaseIDs) > 0 {
				sort.Strings(t.QaseProjects)
				logrus.Infof("Title fallback matched %d cases for %s → projects %v",
					len(t.QaseCaseIDs), t.File, t.QaseProjects)
				testsWithCases++
				testsWithoutCases--
			}
		}
		logrus.Infof("Title fallback resolved %d additional case(s)", fallbackResolved)
	}

	// If no tests have case IDs after both passes, skip validation.
	if testsWithCases == 0 {
		logrus.Warn("No identified tests have Qase cases mapped — Qase validation skipped")
		return nil
	}

	// Second pass: validate case IDs exist in Qase via API.
	validCasesByProject := map[string]map[int]bool{}

	for project := range projectSet {
		logrus.Infof("Validating Qase cases for project %s...", project)
		nameMap, err := qaseClient.GetAutomationNameMap(ctx, project)
		if err != nil {
			return fmt.Errorf("fetching Qase cases for project %s: %w", project, err)
		}
		// Build a set of valid case IDs from the API response.
		validIDs := map[int]bool{}
		for _, id := range nameMap {
			validIDs[id] = true
		}
		validCasesByProject[project] = validIDs
		logrus.Infof("  %s: %d valid cases in Qase", project, len(validIDs))
	}

	// Validate each test's case IDs against the API and assign per-project.
	totalValidated := 0
	totalInvalid := 0
	for i := range result.Tests {
		t := &result.Tests[i]
		if len(t.QaseCaseIDs) == 0 {
			continue
		}

		var validIDs []int
		casesByProject := map[string][]int{}

		for _, caseID := range t.QaseCaseIDs {
			confirmed := false
			for _, project := range t.QaseProjects {
				if validSet, ok := validCasesByProject[project]; ok {
					if validSet[caseID] {
						confirmed = true
						validIDs = append(validIDs, caseID)
						casesByProject[project] = append(casesByProject[project], caseID)
						totalValidated++
						break
					}
				}
			}
			if !confirmed {
				logrus.Warnf("Qase case ID %d (file: %s) not found in any project — removing from test entry", caseID, t.File)
				totalInvalid++
			}
		}
		t.QaseCaseIDs = validIDs
		t.QaseCasesByProject = casesByProject
	}

	logrus.Infof("Qase validation complete: %d cases confirmed, %d invalid/removed", totalValidated, totalInvalid)
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

// buildCandidateNames returns the ordered list of automation name strings to
// try when looking up a TestEntry in Qase maps.
func buildCandidateNames(t *types.TestEntry) []string {
	var names []string
	seen := map[string]struct{}{}
	add := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		names = append(names, s)
	}

	// Most specific: Suite/Function combos (matches gotestsum output format).
	for _, fn := range t.Functions {
		if t.Suite != "" {
			add(t.Suite + "/" + fn)
		}
		add(fn)
	}
	// Suite alone.
	add(t.Suite)

	return names
}
