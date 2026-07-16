package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
)

const (
	genPipelineEnvOutputFlag = "output-file"
)

var genPipelineEnvOutput string

func init() {
	f := genPipelineEnvCmd.Flags()
	f.StringVar(&genPipelineEnvOutput, genPipelineEnvOutputFlag, "pipeline_env.json",
		"Path to write the generated pipeline_env.json template")

	rootCmd.AddCommand(genPipelineEnvCmd)
}

var genPipelineEnvCmd = &cobra.Command{
	Use:   genPipelineEnvCommandName,
	Short: "Generate a pipeline_env.json template with organisation-specific defaults",
	Long: `Writes a pipeline_env.json template pre-populated with the Rancher/SUSE
defaults that were previously compiled into the binary.

pipeline_env.json externalises every organisation- and environment-specific
value that the pipeline needs:

  • Qase custom field ID for AutomationTestName
  • Qase project codes and display names
  • Build-tag → Jenkins job mapping
  • Build-tag / job-name → Qase project mapping
  • GitHub repository slugs (product repo, tests repo)
  • GitHub Copilot bot username
  • GitHub issue label
  • Project display name injected into LLM prompts
  • Cattle-config template: rancher server, cloud credentials, provider
    machine configs, registries and SSH — used by plan-environment to render a
    complete cattle-config.yaml with ${VAR} placeholders for secrets
  • Instance-type catalog: candidate provider instance types used by
    plan-environment --recommend-specs to size nodes
  • Sizing policy: named profiles (minimal/balanced/ha) with HA floors and cost
    caps applied by plan-environment --sizing-profile to upstream/downstream
  • Upstream cluster config: provider/distro/version and qa-infra terraform.tfvars
    template for the recommended Rancher management cluster

The file is intentionally excluded from VCS (.gitignore covers *_env.json).
Supply it to the pipeline at runtime via the --pipeline-env flag, or as a
Jenkins text parameter (the same pattern used for jenkins_trigger_mapping.json).

Customise the generated file for your environment before committing it to a
private secrets store or passing it as a pipeline parameter.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		template := envconfig.Generate()

		data, err := json.MarshalIndent(template, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling pipeline_env template: %w", err)
		}

		if genPipelineEnvOutput == "-" {
			fmt.Println(string(data))
			return nil
		}

		if err := ensureOutputParent(genPipelineEnvOutput); err != nil {
			return err
		}
		if err := os.WriteFile(genPipelineEnvOutput, data, 0600); err != nil {
			return fmt.Errorf("writing pipeline_env to %q: %w", genPipelineEnvOutput, err)
		}

		logrus.Infof("Generated pipeline_env.json template → %s", genPipelineEnvOutput)
		logrus.Info("Review and customise the file for your environment, then supply it")
		logrus.Info("via --pipeline-env at runtime. Do NOT commit it to VCS.")
		return nil
	},
}
