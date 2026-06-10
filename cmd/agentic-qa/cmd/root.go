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
)

var rootCmd = &cobra.Command{
	Use:   "agentic-qa",
	Short: "Agentic QA pipeline CLI for Rancher test automation",
	Long: `agentic-qa orchestrates the full QA pipeline:
  1. identify  - Identify tests relevant to a PR
  2. trigger   - Trigger Jenkins test jobs
  3. wait      - Wait for test completion
  4. analyze   - Triage test failures
  5. defects   - Handle defects (issues, PRs)
  6. config-failures - Handle configuration failures
  7. cleanup   - Clean up pipeline-created resources
  8. validate  - Validate credential connectivity`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		switch provider {
		case "vertex-ai":
			creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
			if creds == "" {
				return fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS environment variable is required for provider %q", provider)
			}
			if vertexProject == "" {
				return fmt.Errorf("--vertex-project is required for provider %q", provider)
			}
		case "claude-direct":
			if os.Getenv("CLAUDE_API_KEY") == "" {
				return fmt.Errorf("CLAUDE_API_KEY environment variable is required for provider %q", provider)
			}
		default:
			return fmt.Errorf("unsupported provider %q: must be vertex-ai or claude-direct", provider)
		}

		logrus.Infof("provider=%s model_sonnet=%s model_haiku=%s qase_project=%s dry_run=%v",
			provider, sonnetModel, haikuModel, qaseProject, dryRun)

		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	pf := rootCmd.PersistentFlags()

	pf.StringVar(&provider, "provider", "vertex-ai", "LLM provider (vertex-ai, claude-direct)")
	pf.StringVar(&vertexProject, "vertex-project", "", "GCP project ID for Vertex AI")
	pf.StringVar(&vertexLocation, "vertex-location", "us-east5", "GCP location for Vertex AI")
	pf.StringVar(&sonnetModel, "sonnet-model", "claude-sonnet-4-6-20250514", "Sonnet model ID")
	pf.StringVar(&haikuModel, "haiku-model", "claude-haiku-3-5-20241022", "Haiku model ID")
	pf.StringVar(&qaseProject, "qase-project", "RANCHERINT", "Qase project code")
	pf.StringVar(&mcpURL, "mcp-url", "", "Qase MCP server URL")
	pf.StringVar(&stateFile, "state-file", "", "Path to pipeline_state.json")
	pf.BoolVar(&dryRun, "dry-run", false, "Dry-run mode (no side effects)")
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
