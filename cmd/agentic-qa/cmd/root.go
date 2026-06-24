package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/llm"
)

var (
	provider        string
	vertexProject   string
	vertexLocation  string
	sonnetModel     string
	haikuModel      string
	qaseProject     string
	mcpURL          string
	stateFile       string
	pipelineEnvFile string
	dryRun          bool
	localTest       bool

	// pipelineEnv is loaded once during PersistentPreRunE and shared across all
	// subcommands.  It is nil when --pipeline-env is not provided, in which case
	// callers must use envconfig.Generate() as a fallback.
	pipelineEnv *envconfig.PipelineEnv
)

const (
	// Command names.
	genFeatureMapCommandName  = "generate-feature-map"
	genTriggerMapCommandName  = "generate-trigger-map"
	genPipelineEnvCommandName = "generate-pipeline-env"
	identifyCommandName       = "identify"
	triggerCommandName        = "trigger"
	waitCommandName           = "wait"
	analyzeCommandName        = "analyze"
	defectsCommandName        = "defects"
	configFailuresCommandName = "config-failures"
	cleanupCommandName        = "cleanup"
	validateCommandName       = "validate"
	completionCommandName     = "completion"

	// Root persistent flag names.
	providerFlag       = "provider"
	vertexProjectFlag  = "vertex-project"
	vertexLocationFlag = "vertex-location"
	sonnetModelFlag    = "sonnet-model"
	haikuModelFlag     = "haiku-model"
	qaseProjectFlag    = "qase-project"
	mcpURLFlag         = "mcp-url"
	stateFileFlag      = "state-file"
	pipelineEnvFlag    = "pipeline-env"
	dryRunFlag         = "dry-run"
	localTestFlag      = "local-test"

	// Flag names shared across multiple subcommands.
	outputFileFlag        = "output-file"
	prNumberFlag          = "pr-number"
	repoFlag              = "repo"
	jenkinsURLFlag        = "jenkins-url"
	triageResultsFlag     = "triage-results"
	pipelineConfigFlag    = "pipeline-config"
	triggerMappingFlag    = "trigger-mapping"
	testsRepoFlag         = "tests-repo"
	autoCreatePRsFlag     = "auto-create-prs"
	qaseProjectsFlag      = "qase-projects"
	additionalContextFlag = "additional-context-file"

	// Environment variable names.
	googleApplicationCredentialsEnvVar = "GOOGLE_APPLICATION_CREDENTIALS"
	claudeAPIKeyEnvVar                 = "CLAUDE_API_KEY"
	qaseApiTokenEnvVar                 = "QASE_API_TOKEN"
	githubTokenEnvVar                  = "GITHUB_TOKEN"
	jenkinsURLEnvVar                   = "JENKINS_URL"
	jenkinsUserEnvVar                  = "JENKINS_USER"
	jenkinsTokenEnvVar                 = "JENKINS_TOKEN"

	// LLM provider names — mirrors llm.ProviderVertexAI / llm.ProviderClaudeDirect.
	// Defined here as local consts so switch statements in this package don't need
	// to import the llm package just for a string comparison.
	llmProviderVertexAI     = llm.ProviderVertexAI
	llmProviderClaudeDirect = llm.ProviderClaudeDirect

	// Default model IDs.
	defaultSonnetModel = "claude-sonnet-4-6-20250514"
	defaultHaikuModel  = "claude-haiku-3-5-20241022"

	// Mapping file version written by both generate-* commands.
	mappingFileVersion = "2.0"

	// Default repository slugs — used only as CLI flag defaults.
	// The actual runtime values come from PipelineEnv.ProductRepo / TestsRepo.
	defaultProductRepo = "rancher/rancher"
	defaultTestsRepo   = "rancher/tests"

	// Job/build status values used in trigger and wait.
	jobStatusSuccess       = "SUCCESS"
	jobStatusFailure       = "FAILURE"
	jobStatusUnstable      = "UNSTABLE"
	jobStatusAborted       = "ABORTED"
	jobStatusInProgress    = "IN_PROGRESS"
	jobStatusTriggerFailed = "trigger_failed"
	jobStatusDryRun        = "dry_run"
	jobStatusLocalTest     = "local_test"
	jobStatusQueued        = "queued"

	// Jenkins job parameter names.
	jenkinsParamTimeout   = "TIMEOUT"
	jenkinsParamPRNumber  = "PR_NUMBER"
	jenkinsParamQaseRunID = "QASE_RUN_ID"
)

// localTestPrefix is prepended to all artifact titles/names when --local-test is set.
const localTestPrefix = "[LOCAL-TEST] "

var rootCmd = &cobra.Command{
	Use:   "agentic-qa",
	Short: "Agentic QA pipeline CLI for Rancher test automation",
	Long: `agentic-qa orchestrates the full QA pipeline:
	1. generate-feature-map - Generate a feature map from test case metadata
	2. generate-trigger-map - Generate a Jenkins trigger mapping from test case metadata
	3. generate-pipeline-env - Generate a pipeline_env.json template with organization-specific defaults
	4. identify  - Identify tests relevant to a PR
  5. trigger   - Trigger Jenkins test jobs
  6. wait      - Wait for test completion
  7. analyze   - Triage test failures
  8. defects   - Handle defects (issues, PRs)
  9. config-failures - Handle configuration failures
  10. cleanup   - Clean up pipeline-created resources
  11. validate  - Validate credential connectivity`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Load pipeline_env.json if provided.  generate-pipeline-env and the
		// other generate-* commands may run without it (they either produce the
		// file or only need the Qase API token, not the full env config).
		if pipelineEnvFile != "" {
			env, err := envconfig.Load(pipelineEnvFile)
			if err != nil {
				return fmt.Errorf("--%s: %w", pipelineEnvFlag, err)
			}
			pipelineEnv = env
			logrus.Infof("Loaded pipeline environment config from %s", pipelineEnvFile)
		}

		// Skip LLM credential validation for commands that don't need it.
		switch cmd.Name() {
		case genFeatureMapCommandName, genTriggerMapCommandName, genPipelineEnvCommandName, validateCommandName, completionCommandName, "help":
			return nil
		}

		switch provider {
		case llmProviderVertexAI:
			creds := os.Getenv(googleApplicationCredentialsEnvVar)
			if creds == "" {
				return fmt.Errorf("%s environment variable is required for provider %q", googleApplicationCredentialsEnvVar, provider)
			}
			if vertexProject == "" {
				return fmt.Errorf("--%s is required for provider %q", vertexProjectFlag, provider)
			}
		case llmProviderClaudeDirect:
			if os.Getenv(claudeAPIKeyEnvVar) == "" {
				return fmt.Errorf("%s environment variable is required for provider %q", claudeAPIKeyEnvVar, provider)
			}
		default:
			return fmt.Errorf("unsupported provider %q: must be %s or %s", provider, llmProviderVertexAI, llmProviderClaudeDirect)
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

	pf.StringVar(&provider, providerFlag, llmProviderVertexAI, "LLM provider (vertex-ai, claude-direct)")
	pf.StringVar(&vertexProject, vertexProjectFlag, "", "GCP project ID for Vertex AI")
	pf.StringVar(&vertexLocation, vertexLocationFlag, "global", "GCP location for Vertex AI")
	pf.StringVar(&sonnetModel, sonnetModelFlag, defaultSonnetModel, "Sonnet model ID")
	pf.StringVar(&haikuModel, haikuModelFlag, defaultHaikuModel, "Haiku model ID")
	pf.StringVar(&qaseProject, qaseProjectFlag, "", "Qase project code")
	pf.StringVar(&mcpURL, mcpURLFlag, "", "Qase MCP server URL")
	pf.StringVar(&stateFile, stateFileFlag, "", "Path to pipeline_state.json")
	pf.StringVar(&pipelineEnvFile, pipelineEnvFlag, "", "Path to pipeline_env.json (organisation-specific config, not committed to VCS)")
	pf.BoolVar(&dryRun, dryRunFlag, false, "Dry-run mode (no side effects)")
	pf.BoolVar(&localTest, localTestFlag, false, "Local test mode: creates real artifacts prefixed with [LOCAL-TEST], creates GitHub PRs as drafts, and skips Jenkins triggers. Use to validate the full pipeline flow without polluting production tracking.")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// activePipelineEnv returns the loaded PipelineEnv if --pipeline-env was
// supplied, otherwise falls back to the generated defaults.  Callers should
// always use this rather than accessing pipelineEnv directly so that the
// pipeline works without a pipeline_env.json during development/testing.
func activePipelineEnv() *envconfig.PipelineEnv {
	if pipelineEnv != nil {
		return pipelineEnv
	}
	return envconfig.Generate()
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
