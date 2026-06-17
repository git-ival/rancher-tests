package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	provider       string
	vertexProject  string
	vertexLocation string
	sonnetModel    string
	haikuModel     string
	qaseProject    string
	mcpURL         string
	stateFile      string
	dryRun         bool
	localTest      bool
)

const (
	genFeatureMapCommandName  = "generate-feature-map"
	genTriggerMapCommandName  = "generate-trigger-map"
	identifyCommandName       = "identify"
	triggerCommandName        = "trigger"
	waitCommandName           = "wait"
	analyzeCommandName        = "analyze"
	defectsCommandName        = "defects"
	configFailuresCommandName = "config-failures"
	cleanupCommandName        = "cleanup"
	validateCommandName       = "validate"
	completionCommandName     = "completion"

	providerFlag       = "provider"
	vertexProjectFlag  = "vertex-project"
	vertexLocationFlag = "vertex-location"
	sonnetModelFlag    = "sonnet-model"
	haikuModelFlag     = "haiku-model"
	qaseProjectFlag    = "qase-project"
	mcpURLFlag         = "mcp-url"
	stateFileFlag      = "state-file"
	dryRunFlag         = "dry-run"
	localTestFlag      = "local-test"

	googleApplicationCredentialsEnvVar = "GOOGLE_APPLICATION_CREDENTIALS"
	claudeAPIKeyEnvVar                 = "CLAUDE_API_KEY"
	qaseApiTokenEnvVar                 = "QASE_API_TOKEN"
)

// localTestPrefix is prepended to all artifact titles/names when --local-test is set.
const localTestPrefix = "[LOCAL-TEST] "

var rootCmd = &cobra.Command{
	Use:   "agentic-qa",
	Short: "Agentic QA pipeline CLI for Rancher test automation",
	Long: `agentic-qa orchestrates the full QA pipeline:
	1. generate-feature-map - Generate a feature map from test case metadata
	2. generate-trigger-map - Generate a Jenkins trigger mapping from test case metadata
	3. identify  - Identify tests relevant to a PR
  4. trigger   - Trigger Jenkins test jobs
  5. wait      - Wait for test completion
  6. analyze   - Triage test failures
  7. defects   - Handle defects (issues, PRs)
  8. config-failures - Handle configuration failures
  9. cleanup   - Clean up pipeline-created resources
  10. validate  - Validate credential connectivity`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Skip LLM credential validation for commands that don't need it.
		switch cmd.Name() {
		case genFeatureMapCommandName, genTriggerMapCommandName, validateCommandName, completionCommandName, "help":
			return nil
		}

		switch provider {
		case "vertex-ai":
			creds := os.Getenv(googleApplicationCredentialsEnvVar)
			if creds == "" {
				return fmt.Errorf("%s environment variable is required for provider %q", googleApplicationCredentialsEnvVar, provider)
			}
			if vertexProject == "" {
				return fmt.Errorf("--%s is required for provider %q", vertexProjectFlag, provider)
			}
		case "claude-direct":
			if os.Getenv(claudeAPIKeyEnvVar) == "" {
				return fmt.Errorf("%s environment variable is required for provider %q", claudeAPIKeyEnvVar, provider)
			}
		default:
			return fmt.Errorf("unsupported provider %q: must be vertex-ai or claude-direct", provider)
		}

		logrus.Infof("provider=%s model_sonnet=%s model_haiku=%s qase_project=%s dry_run=%v local_test=%v",
			provider, sonnetModel, haikuModel, qaseProject, dryRun, localTest)

		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	pf := rootCmd.PersistentFlags()

	pf.StringVar(&provider, providerFlag, "vertex-ai", "LLM provider (vertex-ai, claude-direct)")
	pf.StringVar(&vertexProject, vertexProjectFlag, "", "GCP project ID for Vertex AI")
	pf.StringVar(&vertexLocation, vertexLocationFlag, "global", "GCP location for Vertex AI")
	pf.StringVar(&sonnetModel, sonnetModelFlag, "claude-sonnet-4-6-20250514", "Sonnet model ID")
	pf.StringVar(&haikuModel, haikuModelFlag, "claude-haiku-3-5-20241022", "Haiku model ID")
	pf.StringVar(&qaseProject, qaseProjectFlag, "", "Qase project code")
	pf.StringVar(&mcpURL, mcpURLFlag, "", "Qase MCP server URL")
	pf.StringVar(&stateFile, stateFileFlag, "", "Path to pipeline_state.json")
	pf.BoolVar(&dryRun, dryRunFlag, false, "Dry-run mode (no side effects)")
	pf.BoolVar(&localTest, localTestFlag, false, "Local test mode: creates real artifacts prefixed with [LOCAL-TEST], creates GitHub PRs as drafts, and skips Jenkins triggers. Use to validate the full pipeline flow without polluting production tracking.")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// loadJSON reads a JSON file and unmarshals it into v.
func loadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	return json.Unmarshal(data, v)
}

// saveJSON marshals v to indented JSON and writes it to path.
func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}
