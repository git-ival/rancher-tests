package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/llm"
	"github.com/rancher/tests/internal/agenticqa/runconfig"
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
	configFile      string
	dryRun          bool
	localTest       bool

	// pipelineEnv is loaded once; nil means callers use generated defaults.
	pipelineEnv *envconfig.PipelineEnv
	runConfig   *runconfig.Config
)

const (
	genFeatureMapCommandName   = "generate-feature-map"
	genTriggerMapCommandName   = "generate-trigger-map"
	genPipelineEnvCommandName  = "generate-pipeline-env"
	identifyCommandName        = "identify"
	planEnvironmentCommandName = "plan-environment"
	triggerCommandName         = "trigger"
	waitCommandName            = "wait"
	analyzeCommandName         = "analyze"
	defectsCommandName         = "defects"
	configFailuresCommandName  = "config-failures"
	cleanupCommandName         = "cleanup"
	validateCommandName        = "validate"
	completionCommandName      = "completion"

	providerFlag       = "provider"
	vertexProjectFlag  = "vertex-project"
	vertexLocationFlag = "vertex-location"
	sonnetModelFlag    = "sonnet-model"
	haikuModelFlag     = "haiku-model"
	qaseProjectFlag    = "qase-project"
	mcpURLFlag         = "mcp-url"
	stateFileFlag      = "state-file"
	pipelineEnvFlag    = "pipeline-env"
	configFlag         = "config"
	dryRunFlag         = "dry-run"
	localTestFlag      = "local-test"

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

	googleApplicationCredentialsEnvVar = "GOOGLE_APPLICATION_CREDENTIALS"
	claudeAPIKeyEnvVar                 = "CLAUDE_API_KEY"
	qaseApiTokenEnvVar                 = "QASE_API_TOKEN"
	githubTokenEnvVar                  = "GITHUB_TOKEN"
	jenkinsURLEnvVar                   = "JENKINS_URL"
	jenkinsUserEnvVar                  = "JENKINS_USER"
	jenkinsTokenEnvVar                 = "JENKINS_TOKEN"

	// Local names avoid importing llm solely for comparisons.
	llmProviderVertexAI     = llm.ProviderVertexAI
	llmProviderClaudeDirect = llm.ProviderClaudeDirect

	defaultSonnetModel = "claude-sonnet-4-6-20250514"
	defaultHaikuModel  = "claude-haiku-3-5-20241022"

	mappingFileVersion = "2.0"

	// CLI defaults; PipelineEnv supplies runtime values.
	defaultProductRepo = "rancher/rancher"
	defaultTestsRepo   = "rancher/tests"

	jobStatusSuccess       = "SUCCESS"
	jobStatusFailure       = "FAILURE"
	jobStatusUnstable      = "UNSTABLE"
	jobStatusAborted       = "ABORTED"
	jobStatusInProgress    = "IN_PROGRESS"
	jobStatusTriggerFailed = "trigger_failed"
	jobStatusDryRun        = "dry_run"
	jobStatusLocalTest     = "local_test"
	jobStatusQueued        = "queued"

	jenkinsParamTimeout   = "TIMEOUT"
	jenkinsParamPRNumber  = "PR_NUMBER"
	jenkinsParamQaseRunID = "QASE_RUN_ID"
)

// localTestPrefix is prepended to all artifact titles/names when --local-test is set.
const localTestPrefix = "[LOCAL-TEST] "

const (
	// Token budgets by expected response size.
	llmMaxTokensIdentify   = 4096
	llmMaxTokensGuard      = 4096
	llmMaxTokensTriage     = 2048
	llmMaxTokensEnvInfer   = 2048
	llmMaxTokensDecision   = 1024
	llmMaxTokensSpecRefine = 1024

	// identifyDiffMaxLen is the maximum number of characters of a PR diff that
	// is sent to the LLM. Longer diffs are truncated.
	identifyDiffMaxLen = 50000

	// defaultPollIntervalSeconds is the default interval between Jenkins build
	// status polls.
	defaultPollIntervalSeconds = 120

	// millisecondsPerMinute converts a duration in ms to minutes.
	millisecondsPerMinute = 60000.0

	// defaultMaxReruns is the default maximum number of reruns allowed per test.
	defaultMaxReruns = 2
)

var rootCmd = &cobra.Command{
	Use:   "agentic-qa",
	Short: "Agentic QA pipeline CLI for Rancher test automation",
	Long: `agentic-qa orchestrates the full QA pipeline:
	1. generate-feature-map - Generate a feature map from test case metadata
	2. generate-trigger-map - Generate a Jenkins trigger mapping from test case metadata
	3. generate-pipeline-env - Generate a pipeline_env.json template with organization-specific defaults
	4. identify  - Identify tests relevant to a PR
  5. plan-environment - Determine the minimum viable test environment for the identified tests
  6. trigger   - Trigger Jenkins test jobs
  7. wait      - Wait for test completion
  8. analyze   - Triage test failures
  9. defects   - Handle defects (issues, PRs)
  10. config-failures - Handle configuration failures
  11. cleanup   - Clean up pipeline-created resources
  12. validate  - Validate credential connectivity`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if configFile != "" {
			cfg, err := runconfig.Load(configFile)
			if err != nil {
				return fmt.Errorf("--%s: %w", configFlag, err)
			}
			runConfig = cfg
			if err := applyRunConfig(cmd, cfg); err != nil {
				return err
			}
			logrus.Infof("Loaded run config from %s", configFile)
		}

		// Generator commands may run without pipeline_env.json.
		if pipelineEnvFile != "" {
			env, err := envconfig.Load(pipelineEnvFile)
			if err != nil {
				return fmt.Errorf("--%s: %w", pipelineEnvFlag, err)
			}
			pipelineEnv = env
			logrus.Infof("Loaded pipeline environment config from %s", pipelineEnvFile)
		}

		switch cmd.Name() {
		case genFeatureMapCommandName, genTriggerMapCommandName, genPipelineEnvCommandName, validateCommandName, completionCommandName, "help":
			return nil
		case planEnvironmentCommandName:
			// Static-only planning does not need LLM credentials.
			if planEnvStaticOnly {
				return nil
			}
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
	pf.StringVar(&configFile, configFlag, "", "Path to agentic-qa.yaml")
	pf.BoolVar(&dryRun, dryRunFlag, false, "Dry-run mode (no side effects)")
	pf.BoolVar(&localTest, localTestFlag, false, "Local test mode: creates real artifacts prefixed with [LOCAL-TEST], creates GitHub PRs as drafts, and skips Jenkins triggers. Use to validate the full pipeline flow without polluting production tracking.")
}

func applyRunConfig(cmd *cobra.Command, cfg *runconfig.Config) error {
	set := func(name, value string) error {
		if value == "" {
			return nil
		}
		flag := cmd.Flag(name)
		if flag == nil || flag.Changed {
			return nil
		}
		if err := flag.Value.Set(value); err != nil {
			return fmt.Errorf("setting --%s from --%s: %w", name, configFlag, err)
		}
		flag.Changed = true
		return nil
	}
	setBool := func(name string, value bool) error {
		if !value {
			return nil
		}
		return set(name, strconv.FormatBool(value))
	}
	setInt := func(name string, value *int) error {
		if value == nil {
			return nil
		}
		return set(name, strconv.Itoa(*value))
	}

	for _, item := range []struct{ name, value string }{
		{stateFileFlag, cfg.Paths.Outputs.State},
		{pipelineEnvFlag, cfg.Paths.Inputs.PipelineEnv},
		{providerFlag, cfg.LLM.Provider},
		{vertexProjectFlag, cfg.LLM.VertexProject},
		{vertexLocationFlag, cfg.LLM.VertexLocation},
		{sonnetModelFlag, cfg.LLM.SonnetModel},
		{haikuModelFlag, cfg.LLM.HaikuModel},
		{qaseProjectFlag, cfg.Qase.Project},
		{mcpURLFlag, cfg.Qase.MCPURL},
	} {
		if err := set(item.name, item.value); err != nil {
			return err
		}
	}
	if err := setBool(dryRunFlag, cfg.Execution.DryRun); err != nil {
		return err
	}
	if err := setBool(localTestFlag, cfg.Execution.LocalTest); err != nil {
		return err
	}

	in, out := cfg.Paths.Inputs, cfg.Paths.Outputs
	var values map[string]string
	switch cmd.Name() {
	case genFeatureMapCommandName:
		values = map[string]string{genFeatureMapValidationDirFlag: in.ValidationDir, genFeatureMapActionsDirFlag: in.ActionsDir, outputFileFlag: out.FeatureMapping}
	case genTriggerMapCommandName:
		values = map[string]string{genTriggerMapJJBDirFlag: in.JJBDir, outputFileFlag: out.TriggerMapping}
	case genPipelineEnvCommandName:
		values = map[string]string{outputFileFlag: out.PipelineEnv}
	case identifyCommandName:
		values = map[string]string{repoFlag: cfg.Source.Repo, identifyMappingFileFlag: in.FeatureMapping, additionalContextFlag: in.AdditionalContext, outputFileFlag: out.IdentifiedTests}
		if cfg.Source.PR != 0 {
			pr := cfg.Source.PR
			if err := setInt(prNumberFlag, &pr); err != nil {
				return err
			}
		}
	case planEnvironmentCommandName:
		values = map[string]string{identifiedTestsFlag: out.IdentifiedTests, planEnvTestRepoFlag: in.TestRepoRoot, planEnvChartsDirFlag: in.ChartsDir, outputFileFlag: out.EnvironmentPlan, planEnvOutputDirFlag: out.EnvironmentArtifacts, planEnvSizingProfileFlag: cfg.Environment.SizingProfile}
		for name, value := range map[string]bool{planEnvStaticOnlyFlag: cfg.Environment.StaticOnly, planEnvRecommendFlag: cfg.Environment.RecommendSpecs, planEnvResolveVersionsFlag: cfg.Environment.ResolveVersions, planEnvIncludePrereleasesFlag: cfg.Environment.IncludePrerelease} {
			if err := setBool(name, value); err != nil {
				return err
			}
		}
	case triggerCommandName:
		values = map[string]string{repoFlag: cfg.Source.Repo, identifiedTestsFlag: out.IdentifiedTests, triggerMappingFlag: in.TriggerMapping, outputFileFlag: out.TriggeredJobs, triggerTestTimeoutFlag: cfg.Execution.TestTimeout, jenkinsURLFlag: cfg.Jenkins.URL}
		if cfg.Source.PR != 0 {
			pr := cfg.Source.PR
			if err := setInt(prNumberFlag, &pr); err != nil {
				return err
			}
		}
	case waitCommandName:
		values = map[string]string{waitTriggeredJobsFlag: out.TriggeredJobs, outputFileFlag: out.CompletedJobs, jenkinsURLFlag: cfg.Jenkins.URL}
	case analyzeCommandName:
		values = map[string]string{analyzeCompletedJobsFlag: out.CompletedJobs, analyzeTriageFrameworkFlag: in.TriageFramework, analyzeFeatureMappingFlag: in.FeatureMapping, additionalContextFlag: in.AdditionalContext, outputFileFlag: out.TriageResults}
	case defectsCommandName:
		values = map[string]string{triageResultsFlag: out.TriageResults, outputFileFlag: out.DefectActions}
	case configFailuresCommandName:
		values = map[string]string{triageResultsFlag: out.TriageResults, triggerMappingFlag: in.TriggerMapping, outputFileFlag: out.ConfigActions, jenkinsURLFlag: cfg.Jenkins.URL}
		if err := setInt(cfMaxRerunsFlag, cfg.Execution.MaxReruns); err != nil {
			return err
		}
	case cleanupCommandName:
		values = map[string]string{outputFileFlag: out.CleanupResult}
	}
	for name, value := range values {
		if err := set(name, value); err != nil {
			return err
		}
	}
	return nil
}

func activeJenkinsUser() string {
	if user := os.Getenv(jenkinsUserEnvVar); user != "" {
		return user
	}
	if runConfig != nil {
		return runConfig.Jenkins.User
	}
	return ""
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// activePipelineEnv returns loaded config or generated defaults.
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
	if err := ensureOutputParent(path); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func ensureOutputParent(path string) error {
	if path == "" || path == "-" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}
	return nil
}
