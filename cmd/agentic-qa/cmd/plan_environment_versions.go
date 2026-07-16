package cmd

import (
	"context"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/envversions"
	ghclient "github.com/rancher/tests/internal/agenticqa/github"
	"github.com/rancher/tests/internal/agenticqa/types"
)

// resolveEnvironmentVersions implements --resolve-versions: it looks up the
// earliest Rancher release tag (within the PR base branch's minor line) that
// contains the PR's merge commit, then derives the Kubernetes and
// cert-manager versions that ship with it. It never fails the plan: any
// missing prerequisite (no repo/merge-commit recorded by `identify`, network
// errors, etc.) is logged and results in a nil return, leaving the caller's
// ${VAR} placeholders untouched.
func resolveEnvironmentVersions(
	ctx context.Context,
	identified types.IdentifiedTests,
	upstreamDistro string,
	downstreamDistros []string,
	cfg envconfig.VersionResolutionConfig,
) *types.VersionResolution {
	if identified.Repo == "" {
		logrus.Warnf("--%s: identified_tests.json has no \"repo\" field; re-run `identify` to record it. Skipping version resolution.", planEnvResolveVersionsFlag)
		return nil
	}

	owner, repo, err := ghclient.ParseRepo(identified.Repo)
	if err != nil {
		logrus.Warnf("--%s: %v. Skipping version resolution.", planEnvResolveVersionsFlag, err)
		return nil
	}

	token := planEnvGithubToken
	if token == "" {
		token = os.Getenv(githubTokenEnvVar)
	}
	if token == "" {
		logrus.Warnf("--%s: no GitHub token from --%s or $%s; proceeding unauthenticated (low rate limits apply)",
			planEnvResolveVersionsFlag, planEnvGithubTokenFlag, githubTokenEnvVar)
	}

	gh := ghclient.NewClient(token)
	httpGetter := envversions.DefaultHTTPGetter{Token: token}

	includePrereleases := planEnvIncludePrereleases || cfg.IncludePrereleasesDefault

	resolver := envversions.NewResolver(gh, httpGetter, envversions.Config{
		Owner:                  owner,
		Repo:                   repo,
		BaseRef:                identified.BaseRef,
		MergeCommitSHA:         identified.MergeCommitSHA,
		IncludePrereleases:     includePrereleases,
		UpstreamDistro:         upstreamDistro,
		DownstreamDistros:      downstreamDistros,
		Overrides:              buildVersionOverrides(cfg.Overrides),
		KDMBaseRawURL:          cfg.KDMBaseRawURL,
		KDMReleasesURL:         cfg.KDMReleasesURL,
		RancherRawBaseURL:      cfg.RancherRawBaseURL,
		CertManagerReleasesURL: cfg.CertManagerReleasesURL,
		DefaultMinor:           cfg.DefaultMinor,
	})

	res := resolver.Resolve(ctx)

	logrus.Infof("Resolved rancher_version=%s (%s), rancher_image_tag=%s (%s), cert_manager_version=%s (%s)",
		res.RancherVersion.Value, res.RancherVersion.Source,
		res.RancherImageTag.Value, res.RancherImageTag.Source,
		res.CertManagerVersion.Value, res.CertManagerVersion.Source)
	for distro, v := range res.KubernetesVersionByDistro {
		logrus.Infof("Resolved %s kubernetes_version=%s (%s)", distro, v.Value, v.Source)
	}

	return res
}

// applyResolvedVersions overwrites the ${VAR} placeholders in upstreamCfg and
// the downstream kubeEnvByDistro map with the concrete values from res.
func applyResolvedVersions(upstreamCfg *envconfig.UpstreamConfig, kubeEnvByDistro map[string]string, res *types.VersionResolution) {
	upstreamCfg.RancherVersion = res.RancherVersion.Value
	upstreamCfg.RancherImageTag = res.RancherImageTag.Value
	upstreamCfg.CertManagerVersion = res.CertManagerVersion.Value
	upstreamCfg.RancherChartRepo = res.RancherChartRepoName
	upstreamCfg.RancherChartRepoURL = res.RancherChartRepoURL

	if v, ok := res.KubernetesVersionByDistro[upstreamCfg.KubernetesDistro]; ok {
		upstreamCfg.KubernetesVersion = v.Value
	}

	for distro, v := range res.KubernetesVersionByDistro {
		if kubeEnvByDistro != nil {
			kubeEnvByDistro[distro] = v.Value
		}
	}
}

// buildVersionOverrides merges the pipeline_env.json version_resolution
// overrides with actual OS environment variables of the same name, with the
// live environment variable winning when both are set.
func buildVersionOverrides(configOverrides map[string]string) map[string]string {
	overrides := make(map[string]string, len(configOverrides))
	for k, v := range configOverrides {
		overrides[k] = v
	}

	candidateEnvVars := []string{
		envversions.EnvRancherVersion,
		envversions.EnvRancherImageTag,
		envversions.EnvCertManagerVersion,
		envversions.EnvVarForDistro(types.DistroRKE2),
		envversions.EnvVarForDistro(types.DistroK3S),
		envversions.EnvVarForDistro(types.DistroRKE1),
	}
	for _, k := range candidateEnvVars {
		if v := os.Getenv(k); v != "" {
			overrides[k] = v
		}
	}
	return overrides
}
