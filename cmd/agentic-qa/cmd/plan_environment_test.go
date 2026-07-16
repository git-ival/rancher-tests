package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rancher/tests/internal/agenticqa/types"
)

// resetPlanEnvFlags restores every plan-environment package-level flag var to
// its zero value after a test, since they are shared cobra flag state.
func resetPlanEnvFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		planEnvIdentifiedTests = ""
		planEnvTestRepoRoot = ""
		planEnvOutputFile = ""
		planEnvOutputDir = ""
		planEnvStaticOnly = false
		planEnvNoCattleConfig = false
		planEnvNoUpstream = false
		planEnvRecommendSpecs = false
		planEnvSizingProfile = ""
		planEnvChartsDir = ""
		planEnvResolveVersions = false
		planEnvIncludePrereleases = false
		planEnvGithubToken = ""
	})
}

// writeIdentifiedTestsFixture writes a minimal identified_tests.json good
// enough for plan-environment to run in --static-only mode.
func writeIdentifiedTestsFixture(t *testing.T, dir string, extra types.IdentifiedTests) string {
	t.Helper()
	fixture := types.IdentifiedTests{
		PRNumber: 123,
		Tests: []types.TestEntry{
			{File: "does/not/exist_test.go"}, // AnalyzeFile treats unreadable files as inconclusive -> default cluster.
		},
	}
	fixture.Repo = extra.Repo
	fixture.MergeCommitSHA = extra.MergeCommitSHA
	fixture.Merged = extra.Merged
	fixture.BaseRef = extra.BaseRef

	path := filepath.Join(dir, "identified_tests.json")
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func TestPlanEnvironment_VersionsRemainPlaceholders_WhenResolveVersionsOff(t *testing.T) {
	resetPlanEnvFlags(t)
	dir := t.TempDir()

	planEnvIdentifiedTests = writeIdentifiedTestsFixture(t, dir, types.IdentifiedTests{})
	planEnvOutputFile = filepath.Join(dir, "environment_plan.json")
	planEnvOutputDir = filepath.Join(dir, "environment-plan")
	planEnvTestRepoRoot = dir
	planEnvStaticOnly = true
	planEnvResolveVersions = false

	planEnvironmentCmd.SetContext(context.Background())
	if err := planEnvironmentCmd.RunE(planEnvironmentCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var plan types.EnvironmentPlan
	data, err := os.ReadFile(planEnvOutputFile)
	if err != nil {
		t.Fatalf("reading plan output: %v", err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}

	if plan.VersionResolution != nil {
		t.Errorf("VersionResolution = %+v, want nil when --resolve-versions is not set", plan.VersionResolution)
	}
	if plan.Upstream == nil {
		t.Fatalf("Upstream is nil")
	}
	if !strings.HasPrefix(plan.Upstream.KubernetesVersion, "${") {
		t.Errorf("Upstream.KubernetesVersion = %q, want an unexpanded ${VAR} placeholder", plan.Upstream.KubernetesVersion)
	}
}

func TestPlanEnvironment_ResolveVersions_DegradesGracefully_WhenIdentifyPredatesRepoField(t *testing.T) {
	resetPlanEnvFlags(t)
	dir := t.TempDir()

	// Simulate an identified_tests.json produced before `identify` started
	// persisting repo/merge-commit info: Repo is empty.
	planEnvIdentifiedTests = writeIdentifiedTestsFixture(t, dir, types.IdentifiedTests{})
	planEnvOutputFile = filepath.Join(dir, "environment_plan.json")
	planEnvOutputDir = filepath.Join(dir, "environment-plan")
	planEnvTestRepoRoot = dir
	planEnvStaticOnly = true
	planEnvResolveVersions = true // opted in, but no repo info available

	planEnvironmentCmd.SetContext(context.Background())
	if err := planEnvironmentCmd.RunE(planEnvironmentCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var plan types.EnvironmentPlan
	data, err := os.ReadFile(planEnvOutputFile)
	if err != nil {
		t.Fatalf("reading plan output: %v", err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}

	// Resolution should be skipped (nil), never fail the plan, and versions
	// should remain placeholders.
	if plan.VersionResolution != nil {
		t.Errorf("VersionResolution = %+v, want nil (no repo info to resolve from)", plan.VersionResolution)
	}
	if plan.Upstream == nil {
		t.Fatalf("Upstream is nil")
	}
	if !strings.HasPrefix(plan.Upstream.KubernetesVersion, "${") {
		t.Errorf("Upstream.KubernetesVersion = %q, want an unexpanded ${VAR} placeholder", plan.Upstream.KubernetesVersion)
	}
}
