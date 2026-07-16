package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/runconfig"
)

func TestApplyRunConfigSetsIdentifyPaths(t *testing.T) {
	cmd := &cobra.Command{Use: identifyCommandName}
	cmd.Flags().String(identifyMappingFileFlag, "", "")
	cmd.Flags().String(additionalContextFlag, "", "")
	cmd.Flags().String(outputFileFlag, "", "")
	cmd.Flags().String(repoFlag, "", "")
	cmd.Flags().Int(prNumberFlag, 0, "")

	cfg := &runconfig.Config{
		Source: runconfig.SourceConfig{Repo: "rancher/rancher", PR: 42},
		Paths: runconfig.PathsConfig{
			Inputs: runconfig.InputPaths{
				FeatureMapping:    "/config/features.json",
				AdditionalContext: "/config/context.txt",
			},
			Outputs: runconfig.OutputPaths{IdentifiedTests: "/work/identified.json"},
		},
	}

	if err := applyRunConfig(cmd, cfg); err != nil {
		t.Fatalf("applyRunConfig: %v", err)
	}
	for flag, want := range map[string]string{
		identifyMappingFileFlag: "/config/features.json",
		additionalContextFlag:   "/config/context.txt",
		outputFileFlag:          "/work/identified.json",
		repoFlag:                "rancher/rancher",
		prNumberFlag:            "42",
	} {
		if got := cmd.Flag(flag).Value.String(); got != want {
			t.Errorf("--%s = %q, want %q", flag, got, want)
		}
		if !cmd.Flag(flag).Changed {
			t.Errorf("--%s was not marked changed", flag)
		}
	}
}

func TestApplyRunConfigPreservesCLIValue(t *testing.T) {
	cmd := &cobra.Command{Use: cleanupCommandName}
	cmd.Flags().String(outputFileFlag, "", "")
	if err := cmd.Flags().Set(outputFileFlag, "/cli/result.json"); err != nil {
		t.Fatal(err)
	}

	cfg := &runconfig.Config{Paths: runconfig.PathsConfig{Outputs: runconfig.OutputPaths{CleanupResult: "/config/result.json"}}}
	if err := applyRunConfig(cmd, cfg); err != nil {
		t.Fatalf("applyRunConfig: %v", err)
	}
	if got := cmd.Flag(outputFileFlag).Value.String(); got != "/cli/result.json" {
		t.Fatalf("--output-file = %q", got)
	}
}

func TestEnsureOutputParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "output.json")
	if err := saveJSON(path, map[string]bool{"ok": true}); err != nil {
		t.Fatalf("saveJSON: %v", err)
	}
}
