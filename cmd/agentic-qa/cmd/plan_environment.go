package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/envplan"
	"github.com/rancher/tests/internal/agenticqa/llm"
	"github.com/rancher/tests/internal/agenticqa/types"
)

var (
	planEnvIdentifiedTests string
	planEnvTestRepoRoot    string
	planEnvOutputFile      string
	planEnvOutputDir       string
	planEnvStaticOnly      bool
	planEnvNoCattleConfig  bool
	planEnvNoUpstream      bool
	planEnvRecommendSpecs  bool
	planEnvSizingProfile   string
	planEnvChartsDir       string
)

const (
	planEnvTestRepoFlag      = "test-repo-root"
	planEnvOutputDirFlag     = "output-dir"
	planEnvStaticOnlyFlag    = "static-only"
	planEnvNoCattleCfgFlag   = "no-cattle-config"
	planEnvNoUpstreamFlag    = "no-upstream"
	planEnvRecommendFlag     = "recommend-specs"
	planEnvSizingProfileFlag = "sizing-profile"
	planEnvChartsDirFlag     = "charts-dir"

	planEnvDefaultOutputDir = "environment-plan"
	planEnvCattleSubdir     = "cattle-config"
	planEnvUpstreamSubdir   = "upstream"
)

func init() {
	f := planEnvironmentCmd.Flags()
	f.StringVar(&planEnvIdentifiedTests, identifiedTestsFlag, "", "Path to identified_tests.json (required)")
	f.StringVar(&planEnvTestRepoRoot, planEnvTestRepoFlag, ".", "Path to the rancher-tests repository root (for static source analysis)")
	f.StringVar(&planEnvOutputFile, outputFileFlag, "", "Path to write environment_plan.json (required)")
	f.StringVar(&planEnvOutputDir, planEnvOutputDirFlag, planEnvDefaultOutputDir,
		fmt.Sprintf("Parent directory for generated artifacts; creates %q/ and %q/ subdirectories beneath it",
			planEnvCattleSubdir, planEnvUpstreamSubdir))
	f.StringVar(&planEnvSizingProfile, planEnvSizingProfileFlag, "",
		"Sizing profile to apply (e.g. minimal, balanced, ha); defaults to the pipeline_env sizing_policy.default_profile")
	f.BoolVar(&planEnvStaticOnly, planEnvStaticOnlyFlag, false, "Disable LLM fallback; use static analysis only")
	f.BoolVar(&planEnvNoCattleConfig, planEnvNoCattleCfgFlag, false, "Do not generate downstream cattle-config.yaml files (emit plan JSON only)")
	f.BoolVar(&planEnvNoUpstream, planEnvNoUpstreamFlag, false, "Do not generate the upstream (Rancher management cluster) recommendation or its qa-infra artifacts")
	f.BoolVar(&planEnvRecommendSpecs, planEnvRecommendFlag, false, "Recommend per-node machine specs (vCPU/memory/disk, instanceType) from heuristics + LLM refinement, and write them into the cattle-config")
	f.StringVar(&planEnvChartsDir, planEnvChartsDirFlag, "", "Path to a local rancher/charts checkout (or its charts/ dir) for resolving Helm-chart resource footprints; falls back to the curated catalog when unset/unavailable")

	_ = planEnvironmentCmd.MarkFlagRequired(identifiedTestsFlag)
	_ = planEnvironmentCmd.MarkFlagRequired(outputFileFlag)

	rootCmd.AddCommand(planEnvironmentCmd)
}

var planEnvironmentCmd = &cobra.Command{
	Use:   planEnvironmentCommandName,
	Short: "Determine the minimum viable test environment for identified tests",
	Long: `Computes the minimum viable test environment (node counts and roles,
Kubernetes distro/version, CNI, provider, networking, downstream cluster
topology and required workloads) needed to run the identified tests.

It statically analyses the test sources in the rancher-tests repository and
falls back to the LLM for tests whose requirements cannot be derived
statically. It first tries to satisfy ALL tests with a single environment; if
that is unsafe (conflicting providers, distros, CNIs, hardened/PSACT), it
falls back to one environment per Jenkins-job/build-tag group.

Outputs environment_plan.json plus, under --output-dir, a cattle-config/
subdirectory with a ready-to-use cattle-config.yaml per downstream environment
group, and an upstream/ subdirectory with qa-infra-automation input files for
the recommended upstream Rancher management cluster (terraform.tfvars and
ansible vars.yaml), unless --no-upstream is set.

The --sizing-profile flag selects a node-sizing policy (e.g. minimal, balanced,
ha, performance) from pipeline_env.json. Profiles raise node counts to meet
high-availability floors (etcd quorum, redundant control-plane/worker) and clamp
resources to cost caps, with HA floors taking precedence over cost caps. Each
profile also expresses an instance-family preference: minimal/balanced/ha prefer
cheaper burstable families (t3a, then t3), while "performance" prefers
non-burstable compute/memory-optimised families (m5, c5, r5) and allows larger
per-node sizes for high-load suites. The default profile is "minimal" (minimum
viable, preserving historical behaviour); consider "balanced" or "ha" for real
pipelines and "performance" for heavy chart-driven suites.

With --recommend-specs it additionally computes recommended per-node machine
specs (vCPU, memory, disk) from role-based heuristics scaled by the group's
workloads, optionally refined by the LLM, and maps them to provider-specific
fields in the cattle-config (AWS instanceType selected from the catalog in
pipeline_env.json plus rootSize/volumeSize; Harvester/vSphere cpuCount/
memorySize/diskSize).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		var identified types.IdentifiedTests
		if err := loadJSON(planEnvIdentifiedTests, &identified); err != nil {
			return fmt.Errorf("loading identified tests: %w", err)
		}
		if len(identified.Tests) == 0 {
			return fmt.Errorf("identified_tests.json contains no tests")
		}

		env := activePipelineEnv()

		// Resolve the cattle-config template. If the loaded pipeline_env.json
		// predates the cattle_config section (or omitted it), env.CattleConfig
		// is empty and would yield a config missing rancher/credentials/
		// machine-config sections. Fall back to the built-in defaults and warn
		// loudly so the operator regenerates their pipeline_env.json.
		cattleTmpl := env.CattleConfig
		if cattleTmpl.IsEmpty() {
			logrus.Warnf("--%s file has no \"cattle_config\" section; falling back to built-in defaults. "+
				"Regenerate it with `agentic-qa generate-pipeline-env` to customise rancher host, "+
				"credentials, provider machine configs, registries and SSH.", pipelineEnvFlag)
			cattleTmpl = envconfig.GenerateCattleConfigTemplate()
		}

		// --recommend-specs needs an instance-type catalog to map abstract
		// specs onto provider instanceTypes. If a customised pipeline_env.json
		// predates the catalog field, fall back to the built-in catalog so
		// instanceType selection still works (rootSize/volumeSize work without
		// it, but instanceType would otherwise stay a placeholder).
		if planEnvRecommendSpecs && len(cattleTmpl.InstanceTypeCatalog) == 0 {
			logrus.Warnf("--%s file has no \"cattle_config.instance_type_catalog\"; using the built-in "+
				"catalog for instanceType selection. Regenerate it with `agentic-qa generate-pipeline-env` "+
				"to customise selectable instance types.", pipelineEnvFlag)
			cattleTmpl.InstanceTypeCatalog = envconfig.GenerateCattleConfigTemplate().InstanceTypeCatalog
		}

		// Resolve the sizing policy. Fall back to built-in defaults when the
		// loaded pipeline_env.json predates the sizing_policy section.
		sizingPolicy := env.SizingPolicy
		if sizingPolicy.IsEmpty() {
			logrus.Warnf("--%s file has no \"sizing_policy\" section; falling back to built-in defaults. "+
				"Regenerate it with `agentic-qa generate-pipeline-env` to customise sizing profiles.", pipelineEnvFlag)
			sizingPolicy = envconfig.GenerateSizingPolicy()
		}
		profile, profileName, ok := sizingPolicy.ResolveProfile(planEnvSizingProfile)
		if !ok {
			return fmt.Errorf("--%s %q is not defined in the sizing policy", planEnvSizingProfileFlag, planEnvSizingProfile)
		}
		logrus.Infof("Using sizing profile %q", profileName)

		// Resolve the upstream config. Fall back to built-in defaults when the
		// loaded pipeline_env.json predates the upstream section.
		upstreamCfg := env.Upstream
		if !planEnvNoUpstream && upstreamCfg.IsEmpty() {
			logrus.Warnf("--%s file has no \"upstream\" section; falling back to built-in defaults. "+
				"Regenerate it with `agentic-qa generate-pipeline-env` to customise the upstream cluster.", pipelineEnvFlag)
			upstreamCfg = envconfig.GenerateUpstreamConfig()
		}

		// Resolve the chart-sizing config (used by --recommend-specs to size
		// clusters from Helm-chart footprints). Fall back to built-in defaults
		// when the loaded pipeline_env.json predates the chart_sizing section.
		chartSizingCfg := env.ChartSizing
		if planEnvRecommendSpecs && chartSizingCfg.IsEmpty() {
			logrus.Warnf("--%s file has no \"chart_sizing\" section; falling back to built-in defaults. "+
				"Regenerate it with `agentic-qa generate-pipeline-env` to customise chart footprints.", pipelineEnvFlag)
			chartSizingCfg = envconfig.GenerateChartSizing()
		}
		if planEnvChartsDir != "" {
			chartSizingCfg.Source.LocalPath = planEnvChartsDir
		}

		// Derived output directories under the consolidated --output-dir.
		cattleConfigDir := filepath.Join(planEnvOutputDir, planEnvCattleSubdir)
		upstreamDir := filepath.Join(planEnvOutputDir, planEnvUpstreamSubdir)

		// Optional LLM client for fallback. Only created when needed.
		var llmClient *llm.Client
		if !planEnvStaticOnly {
			c, err := newLLMClient(ctx, haikuModel)
			if err != nil {
				return fmt.Errorf("creating LLM client (use --%s to skip): %w", planEnvStaticOnlyFlag, err)
			}
			llmClient = c
		}

		reqs := make([]envplan.Requirement, 0, len(identified.Tests))
		staticCount, llmCount, defaultCount := 0, 0, 0

		for _, t := range identified.Tests {
			req := envplan.Requirement{
				File:      t.File,
				Jobs:      append([]string(nil), t.QaseProjects...), // placeholder, replaced below
				BuildTags: append([]string(nil), t.BuildTags...),
			}
			// Resolve Jenkins jobs for grouping: prefer the plan-level
			// recommended jobs intersected with this test's build tags via the
			// pipeline env tag→job map.
			req.Jobs = jenkinsJobsForTest(t, identified, env.TagToJob)

			analysis := envplan.AnalyzeFile(planEnvTestRepoRoot, t.File)

			if !analysis.Inconclusive {
				req.Cluster = analysis.Cluster
				req.Workloads = analysis.Workloads
				req.Charts = analysis.Charts
				req.DerivedBy = types.DerivedByStatic
				staticCount++
			} else if llmClient != nil {
				cluster, workloads, ok := inferWithLLM(ctx, llmClient, env.ProjectDisplayName, planEnvTestRepoRoot, t.File)
				if ok {
					req.Cluster = cluster
					req.Workloads = workloads
					req.DerivedBy = types.DerivedByLLM
					llmCount++
				} else {
					req.Cluster = defaultCluster()
					req.DerivedBy = types.DerivedByDefault
					defaultCount++
				}
			} else {
				// Static-only mode and inconclusive: apply a safe default.
				req.Cluster = defaultCluster()
				req.DerivedBy = types.DerivedByDefault
				defaultCount++
			}

			reqs = append(reqs, req)
		}

		plan := envplan.BuildPlan(reqs, identified.PRNumber)
		plan.Metadata = types.MappingMetadata{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			GeneratedBy: "agentic-qa " + planEnvironmentCommandName,
			Version:     mappingFileVersion,
		}

		logrus.Infof("Derived environment for %d test(s): %d static, %d llm, %d default → strategy=%s, %d group(s)",
			len(reqs), staticCount, llmCount, defaultCount, plan.Strategy, len(plan.Groups))

		// Compute recommended specs per group: resolve each group's merged Helm
		// chart footprints (the dominant resource driver), then size pools from
		// role baselines + workload pressure + chart footprints. Done per group
		// (post-merge) because charts are group-level.
		if planEnvRecommendSpecs {
			resolver := envplan.NewChartFootprintResolver(chartSizingCfg, upstreamCfg.RancherVersion)
			logrus.Infof("Using %s", resolver)
			for i := range plan.Groups {
				g := &plan.Groups[i]
				resolveGroupCharts(ctx, g, resolver, llmClient, env.ProjectDisplayName)
				g.Cluster = envplan.ComputeSpecsWithCharts(g.Cluster, g.Workloads, g.Charts)
			}
		}

		// Optional LLM spec refinement, one call per group. The heuristic spec
		// is the floor; the LLM may only raise it (enforced in
		// ApplySpecRefinement). Skipped in --static-only mode.
		if planEnvRecommendSpecs && llmClient != nil {
			for i := range plan.Groups {
				g := &plan.Groups[i]
				sys := envplan.BuildSpecRefinementSystemPrompt(env.ProjectDisplayName)
				user := envplan.BuildSpecRefinementUserMessage(*g)
				var res envplan.SpecRefinementResult
				if err := llmClient.CompleteJSON(ctx, sys, user, llmMaxTokensSpecRefine, &res); err != nil {
					logrus.Warnf("Spec refinement failed for group %q (keeping heuristics): %v", g.Name, err)
					continue
				}
				n := envplan.ApplySpecRefinement(g, res)
				logrus.Infof("LLM refined specs for %d pool(s) in group %q", n, g.Name)
			}
		} else if planEnvRecommendSpecs {
			logrus.Info("Recommending specs from heuristics only (LLM refinement disabled by --static-only)")
		}

		// Apply the downstream sizing policy to each group: HA floors, odd-etcd,
		// per-node caps, total-node cap. Done after spec computation so caps can
		// clamp recommended specs, and before cattle-config generation so the
		// emitted quantities reflect the policy.
		for i := range plan.Groups {
			g := &plan.Groups[i]
			adjusted, warnings := envplan.ApplySizingPolicy(g.Cluster, profile.Downstream)
			g.Cluster = adjusted
			g.AppliedProfile = profileName
			g.Warnings = append(g.Warnings, warnings...)
			for _, w := range warnings {
				logrus.Warnf("group %q: %s", g.Name, w)
			}
		}

		// Generate downstream cattle-config files per group.
		if !planEnvNoCattleConfig {
			if err := os.MkdirAll(cattleConfigDir, 0o755); err != nil {
				return fmt.Errorf("creating cattle-config dir: %w", err)
			}
			for i := range plan.Groups {
				g := &plan.Groups[i]
				data, err := envplan.GenerateCattleConfig(*g, cattleTmpl, profile.Downstream.PreferredFamilies)
				if err != nil {
					return fmt.Errorf("generating cattle-config for group %q: %w", g.Name, err)
				}
				fileName := fmt.Sprintf("cattle-config-%s.yaml", sanitizeGroupName(g.Name))
				outPath := filepath.Join(cattleConfigDir, fileName)
				if err := os.WriteFile(outPath, data, 0o644); err != nil {
					return fmt.Errorf("writing cattle-config %s: %w", outPath, err)
				}
				g.CattleConfigPath = outPath
				logrus.Infof("Wrote cattle-config for group %q (%d nodes) → %s",
					g.Name, g.Cluster.TotalNodes, outPath)
			}
		}

		// Build and emit the upstream (Rancher management) cluster recommendation.
		if !planEnvNoUpstream {
			upstream, err := buildAndEmitUpstream(upstreamCfg, profile.Upstream, profileName, upstreamDir, planEnvRecommendSpecs, cattleTmpl, profile.Upstream.PreferredFamilies)
			if err != nil {
				return err
			}
			plan.Upstream = upstream
		}

		if err := saveJSON(planEnvOutputFile, plan); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		logrus.Infof("Environment plan written → %s", planEnvOutputFile)
		return nil
	},
}

// buildAndEmitUpstream constructs the upstream cluster recommendation from a
// fixed baseline shaped by the sizing policy, optionally sizes it, writes the
// qa-infra-automation artifacts (mirroring qa-infra's relative paths under
// upstreamDir), and returns the populated UpstreamCluster for the plan JSON.
func buildAndEmitUpstream(
	cfg envconfig.UpstreamConfig,
	spec envconfig.SizingTargetSpec,
	profileName, upstreamDir string,
	recommendSpecs bool,
	cattleTmpl envconfig.CattleConfigTemplate,
	preferredFamilies []string,
) (*types.UpstreamCluster, error) {
	up := envplan.DefaultUpstreamCluster(cfg)

	// Optionally compute per-node specs for the upstream nodes too, so the
	// emitted tfvars can carry a concrete instance type / volume size.
	if recommendSpecs {
		cluster := types.ClusterRequirement{NodePools: up.NodePools}
		cluster = envplan.ComputeSpecs(cluster, nil)
		// Resolve instance types from the catalog for the upstream provider.
		for i := range cluster.NodePools {
			if s := cluster.NodePools[i].Spec; s != nil {
				if it, ok := cattleTmpl.SelectInstanceTypeForSpec(cfg.Provider, s.VCPUs, s.MemoryGiB, preferredFamilies); ok {
					s.InstanceType = it.Name
				}
			}
		}
		up.NodePools = cluster.NodePools
	}

	up, warnings := envplan.ApplyUpstreamPolicy(up, spec)
	up.AppliedProfile = profileName
	up.Warnings = append(up.Warnings, warnings...)
	for _, w := range warnings {
		logrus.Warnf("upstream cluster: %s", w)
	}

	if err := os.MkdirAll(upstreamDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating upstream dir: %w", err)
	}

	tfvarsPath, clusterVarsPath, rancherVarsPath := envplan.UpstreamArtifactPaths(cfg)

	// terraform.tfvars (AWS only).
	if envplan.SupportsTfvars(cfg) {
		data, err := envplan.GenerateTerraformTfvars(up, cfg)
		if err != nil {
			return nil, fmt.Errorf("generating upstream terraform.tfvars: %w", err)
		}
		if err := writeUpstreamArtifact(upstreamDir, tfvarsPath, data); err != nil {
			return nil, err
		}
		up.ArtifactPaths = append(up.ArtifactPaths, tfvarsPath)
	} else {
		logrus.Warnf("upstream provider %q has no qa-infra cluster_nodes tofu module; skipping terraform.tfvars (ansible vars still emitted)", cfg.Provider)
	}

	// ansible cluster vars.yaml.
	clusterVars, err := envplan.GenerateClusterVarsYAML(cfg)
	if err != nil {
		return nil, fmt.Errorf("generating upstream cluster vars.yaml: %w", err)
	}
	if err := writeUpstreamArtifact(upstreamDir, clusterVarsPath, clusterVars); err != nil {
		return nil, err
	}
	up.ArtifactPaths = append(up.ArtifactPaths, clusterVarsPath)

	// ansible rancher vars.yaml (always emitted).
	rancherVars, err := envplan.GenerateRancherVarsYAML(cfg)
	if err != nil {
		return nil, fmt.Errorf("generating upstream rancher vars.yaml: %w", err)
	}
	if err := writeUpstreamArtifact(upstreamDir, rancherVarsPath, rancherVars); err != nil {
		return nil, err
	}
	up.ArtifactPaths = append(up.ArtifactPaths, rancherVarsPath)

	logrus.Infof("Wrote upstream cluster artifacts (%d nodes, %d file(s)) → %s",
		up.TotalNodes, len(up.ArtifactPaths), upstreamDir)
	return &up, nil
}

// writeUpstreamArtifact writes data to relPath under baseDir, creating any
// intermediate directories needed to mirror the qa-infra layout.
func writeUpstreamArtifact(baseDir, relPath string, data []byte) error {
	outPath := filepath.Join(baseDir, relPath)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("creating dir for %s: %w", outPath, err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	return nil
}

// resolveGroupCharts resolves the resource footprint of every Helm chart in a
// group, in place. It first uses the non-LLM tiers (local Chart.yaml
// annotations, then the curated catalog); any charts still unresolved are sent
// to the LLM for estimation (unless the client is nil, i.e. --static-only).
func resolveGroupCharts(
	ctx context.Context,
	g *types.EnvironmentGroup,
	resolver *envplan.ChartFootprintResolver,
	llmClient *llm.Client,
	projectDisplayName string,
) {
	if len(g.Charts) == 0 {
		return
	}
	resolved, unresolved := resolver.ResolveCharts(g.Charts)
	logrus.Infof("group %q: resolved %d/%d chart footprint(s) from annotations/catalog",
		g.Name, resolved, len(g.Charts))

	if len(unresolved) == 0 || llmClient == nil {
		if len(unresolved) > 0 {
			logrus.Warnf("group %q: %d chart(s) have no resolved footprint and LLM is disabled; "+
				"using conservative defaults: %s", g.Name, len(unresolved), strings.Join(unresolved, ", "))
		}
		return
	}

	// LLM fallback for the remaining charts.
	sys := envplan.BuildChartFootprintSystemPrompt(projectDisplayName)
	user := envplan.BuildChartFootprintUserMessage(unresolved, resolver.ReleaseBranch())
	var res envplan.ChartFootprintLLMResult
	if err := llmClient.CompleteJSON(ctx, sys, user, llmMaxTokensSpecRefine, &res); err != nil {
		logrus.Warnf("group %q: chart footprint LLM estimation failed (using defaults): %v", g.Name, err)
		return
	}
	n := envplan.ApplyChartFootprintLLM(g.Charts, res)
	logrus.Infof("group %q: LLM estimated %d chart footprint(s)", g.Name, n)
}

// jenkinsJobsForTest resolves the Jenkins jobs that will run a given test, used
// for per-group fallback. It prefers the plan-level RecommendedJobs, and also
// maps each of the test's build tags to a job via the pipeline env tag→job map.
func jenkinsJobsForTest(t types.TestEntry, identified types.IdentifiedTests, tagToJob map[string]string) []string {
	seen := map[string]struct{}{}
	var jobs []string
	add := func(j string) {
		if j == "" {
			return
		}
		if _, ok := seen[j]; ok {
			return
		}
		seen[j] = struct{}{}
		jobs = append(jobs, j)
	}
	for _, tag := range t.BuildTags {
		if j, ok := tagToJob[tag]; ok {
			add(j)
		}
	}
	// If this test maps to no job via its tags, fall back to the plan-level
	// recommended jobs so it still gets grouped.
	if len(jobs) == 0 {
		for _, j := range identified.RecommendedJobs {
			add(j)
		}
	}
	sort.Strings(jobs)
	return jobs
}

// inferWithLLM runs the LLM fallback for one test file. Returns ok=false when
// the source cannot be read or the LLM call fails (caller applies a default).
func inferWithLLM(ctx context.Context, client *llm.Client, projectDisplayName, repoRoot, relPath string) (types.ClusterRequirement, []types.WorkloadRequirement, bool) {
	data, err := os.ReadFile(filepath.Join(repoRoot, relPath))
	if err != nil {
		logrus.Warnf("LLM fallback: cannot read %s: %v", relPath, err)
		return types.ClusterRequirement{}, nil, false
	}
	sys := envplan.BuildLLMSystemPrompt(projectDisplayName)
	user := envplan.BuildLLMUserMessage(relPath, string(data))

	var res envplan.LLMResult
	if err := client.CompleteJSON(ctx, sys, user, llmMaxTokensEnvInfer, &res); err != nil {
		logrus.Warnf("LLM fallback failed for %s: %v", relPath, err)
		return types.ClusterRequirement{}, nil, false
	}
	cluster, workloads := res.ToClusterRequirement()
	logrus.Debugf("LLM derived environment for %s: %d nodes, distro=%s provider=%s",
		relPath, cluster.TotalNodes, cluster.KubernetesDistro, cluster.Provider)
	return cluster, workloads, true
}

// defaultCluster is the conservative fallback: a single all-roles downstream
// node. This is the smallest cluster that can run the majority of provisioning
// and workload tests.
func defaultCluster() types.ClusterRequirement {
	return types.ClusterRequirement{
		NodePools: []types.NodeRequirement{
			{Etcd: true, ControlPlane: true, Worker: true, Quantity: 1, Description: "default all-roles"},
		},
		Downstream: true,
		TotalNodes: 1,
	}
}

// sanitizeGroupName makes a group name safe for use in a filename.
func sanitizeGroupName(name string) string {
	r := strings.NewReplacer("/", "-", " ", "-", ":", "-", "\\", "-", "=", "-", ",", "-")
	return r.Replace(name)
}
