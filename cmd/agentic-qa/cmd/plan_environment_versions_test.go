package cmd

import (
	"context"
	"os"
	"testing"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/types"
)

func TestApplyResolvedVersions(t *testing.T) {
	upstreamCfg := envconfig.UpstreamConfig{
		KubernetesDistro:   "rke2",
		KubernetesVersion:  "${RKE2_VERSION}",
		RancherVersion:     "${RANCHER_VERSION}",
		RancherImageTag:    "${RANCHER_IMAGE_TAG}",
		CertManagerVersion: "${CERT_MANAGER_VERSION}",
	}
	kubeEnvByDistro := map[string]string{
		"rke2": "${RKE2_VERSION}",
		"k3s":  "${K3S_VERSION}",
	}
	res := &types.VersionResolution{
		RancherVersion:     types.ResolvedVersion{Value: "v2.14.1", Source: "tag-compare"},
		RancherImageTag:    types.ResolvedVersion{Value: "v2.14.1", Source: "tag-compare"},
		CertManagerVersion: types.ResolvedVersion{Value: "1.14.5", Source: "release-notes"},
		KubernetesVersionByDistro: map[string]types.ResolvedVersion{
			"rke2": {Value: "v1.31.5+rke2r1", Source: "kdm"},
			"k3s":  {Value: "v1.31.5+k3s1", Source: "kdm"},
		},
	}

	applyResolvedVersions(&upstreamCfg, kubeEnvByDistro, res)

	if upstreamCfg.RancherVersion != "v2.14.1" {
		t.Errorf("RancherVersion = %q, want v2.14.1", upstreamCfg.RancherVersion)
	}
	if upstreamCfg.RancherImageTag != "v2.14.1" {
		t.Errorf("RancherImageTag = %q, want v2.14.1", upstreamCfg.RancherImageTag)
	}
	if upstreamCfg.CertManagerVersion != "1.14.5" {
		t.Errorf("CertManagerVersion = %q, want 1.14.5", upstreamCfg.CertManagerVersion)
	}
	if upstreamCfg.KubernetesVersion != "v1.31.5+rke2r1" {
		t.Errorf("KubernetesVersion = %q, want v1.31.5+rke2r1 (upstream distro is rke2)", upstreamCfg.KubernetesVersion)
	}
	if kubeEnvByDistro["rke2"] != "v1.31.5+rke2r1" {
		t.Errorf("kubeEnvByDistro[rke2] = %q, want v1.31.5+rke2r1", kubeEnvByDistro["rke2"])
	}
	if kubeEnvByDistro["k3s"] != "v1.31.5+k3s1" {
		t.Errorf("kubeEnvByDistro[k3s] = %q, want v1.31.5+k3s1", kubeEnvByDistro["k3s"])
	}
	if upstreamCfg.RancherChartRepo != "" || upstreamCfg.RancherChartRepoURL != "" {
		t.Errorf("expected no chart repo override for a full release, got repo=%q url=%q", upstreamCfg.RancherChartRepo, upstreamCfg.RancherChartRepoURL)
	}
}

func TestApplyResolvedVersions_SetsChartRepoOverrideForAlpha(t *testing.T) {
	upstreamCfg := envconfig.UpstreamConfig{KubernetesDistro: "rke2"}
	res := &types.VersionResolution{
		RancherVersion:       types.ResolvedVersion{Value: "v2.15.0-alpha19", Source: "tag-compare-prerelease"},
		RancherChartRepoName: "rancher-alpha",
		RancherChartRepoURL:  "https://releases.rancher.com/server-charts/alpha",
	}

	applyResolvedVersions(&upstreamCfg, nil, res)

	if upstreamCfg.RancherChartRepo != "rancher-alpha" {
		t.Errorf("RancherChartRepo = %q, want rancher-alpha", upstreamCfg.RancherChartRepo)
	}
	if upstreamCfg.RancherChartRepoURL != "https://releases.rancher.com/server-charts/alpha" {
		t.Errorf("RancherChartRepoURL = %q, want the alpha channel URL", upstreamCfg.RancherChartRepoURL)
	}
}

func TestApplyResolvedVersions_UnknownUpstreamDistroLeavesKubernetesVersionUntouched(t *testing.T) {
	upstreamCfg := envconfig.UpstreamConfig{
		KubernetesDistro:  "rke1", // not present in KubernetesVersionByDistro
		KubernetesVersion: "${RKE1_VERSION}",
	}
	res := &types.VersionResolution{
		KubernetesVersionByDistro: map[string]types.ResolvedVersion{
			"rke2": {Value: "v1.31.5+rke2r1", Source: "kdm"},
		},
	}

	applyResolvedVersions(&upstreamCfg, nil, res)

	if upstreamCfg.KubernetesVersion != "${RKE1_VERSION}" {
		t.Errorf("KubernetesVersion = %q, want unchanged placeholder", upstreamCfg.KubernetesVersion)
	}
}

func TestBuildVersionOverrides_EnvWinsOverConfig(t *testing.T) {
	t.Setenv("RANCHER_VERSION", "v2.14.9-from-env")
	os.Unsetenv("RKE2_VERSION")
	os.Unsetenv("CERT_MANAGER_VERSION")
	os.Unsetenv("RANCHER_IMAGE_TAG")
	os.Unsetenv("K3S_VERSION")
	os.Unsetenv("RKE1_VERSION")

	configOverrides := map[string]string{
		"RANCHER_VERSION": "v2.14.0-from-config",
		"RKE2_VERSION":    "v1.30.5-from-config",
	}

	got := buildVersionOverrides(configOverrides)

	if got["RANCHER_VERSION"] != "v2.14.9-from-env" {
		t.Errorf("RANCHER_VERSION = %q, want env value to win", got["RANCHER_VERSION"])
	}
	if got["RKE2_VERSION"] != "v1.30.5-from-config" {
		t.Errorf("RKE2_VERSION = %q, want config value preserved (no env override set)", got["RKE2_VERSION"])
	}
}

func TestBuildVersionOverrides_NoConfigNoEnv(t *testing.T) {
	for _, k := range []string{"RANCHER_VERSION", "RANCHER_IMAGE_TAG", "CERT_MANAGER_VERSION", "RKE2_VERSION", "K3S_VERSION", "RKE1_VERSION"} {
		os.Unsetenv(k)
	}
	got := buildVersionOverrides(nil)
	if len(got) != 0 {
		t.Errorf("buildVersionOverrides(nil) = %v, want empty map", got)
	}
}

func TestResolveEnvironmentVersions_NoRepo_ReturnsNilWithoutNetworkCalls(t *testing.T) {
	identified := types.IdentifiedTests{PRNumber: 42} // Repo deliberately empty
	res := resolveEnvironmentVersions(context.Background(), identified, "rke2", []string{"k3s"}, envconfig.VersionResolutionConfig{})
	if res != nil {
		t.Errorf("resolveEnvironmentVersions with no Repo = %+v, want nil", res)
	}
}

func TestResolveEnvironmentVersions_InvalidRepo_ReturnsNilWithoutNetworkCalls(t *testing.T) {
	identified := types.IdentifiedTests{PRNumber: 42, Repo: "not-a-valid-repo-slug"}
	res := resolveEnvironmentVersions(context.Background(), identified, "rke2", []string{"k3s"}, envconfig.VersionResolutionConfig{})
	if res != nil {
		t.Errorf("resolveEnvironmentVersions with invalid Repo = %+v, want nil", res)
	}
}
