// Package envconfig manages Agentic QA runtime configuration.
package envconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rancher/tests/internal/agenticqa/qase"
	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	// defaultAWSCICDInstanceTag is the EC2 instance tag applied to all
	// pipeline-created AWS instances so they can be identified as CI workloads.
	defaultAWSCICDInstanceTag = "platform-qa"
)

// PipelineEnv contains environment-specific configuration.
type PipelineEnv struct {
	// QaseATNFieldID identifies the AutomationTestName field.
	QaseATNFieldID int `json:"qase_atn_field_id"`

	// QaseProjects lists projects in query order.
	QaseProjects []string `json:"qase_projects"`

	// QaseProjectNames maps project codes to display names.
	QaseProjectNames map[string]string `json:"qase_project_names"`

	// TagToJob maps build tags to canonical Jenkins jobs.
	TagToJob map[string]string `json:"tag_to_job"`

	// TagToQaseProject maps a build tag prefix or full tag to the Qase project
	// code that should receive results for jobs triggered by that tag.
	// Keys may be full tags ("pit.daily") or prefixes ("pit.") — the lookup
	// tries the full tag first, then the longest matching prefix.
	TagToQaseProject map[string]string `json:"tag_to_qase_project"`

	// JobNamePatternToQaseProject maps case-insensitive job substrings to projects.
	JobNamePatternToQaseProject map[string]string `json:"job_name_pattern_to_qase_project"`

	// JobNameTagPatterns contains ordered, case-insensitive tag inference rules.
	JobNameTagPatterns []JobNameTagPattern `json:"job_name_tag_patterns"`

	// DefaultQaseProject is the Qase project code to assign when no tag or
	// job-name pattern produces a match.
	DefaultQaseProject string `json:"default_qase_project"`

	// ProductRepo receives product-defect issues.
	ProductRepo string `json:"product_repo"`

	// TestsRepo receives test issues and config-failure PRs.
	TestsRepo string `json:"tests_repo"`

	// CopilotUsername receives generated defect issues.
	CopilotUsername string `json:"copilot_username"`

	// AgenticQALabel marks generated issues and PRs.
	AgenticQALabel string `json:"agentic_qa_label"`

	// ProjectDisplayName supplies product context to prompts.
	ProjectDisplayName string `json:"project_display_name"`

	// CattleConfig configures generated cattle-config.yaml values.
	// Placeholders need envsubst; shepherd does not expand them.
	CattleConfig CattleConfigTemplate `json:"cattle_config"`

	// SizingPolicy defines the node-count/HA/cost profiles applied by
	// plan-environment to upstream and downstream cluster topologies.
	SizingPolicy SizingPolicyConfig `json:"sizing_policy"`

	// Upstream holds the settings used to render the upstream Rancher
	// management cluster recommendation into qa-infra-automation input files.
	Upstream UpstreamConfig `json:"upstream"`

	// ChartSizing configures how plan-environment resolves the resource
	// footprint of Helm charts the tests install (used to size downstream
	// clusters). It carries the live chart source and a curated fallback
	// catalog.
	ChartSizing ChartSizingConfig `json:"chart_sizing"`

	// VersionResolution configures plan-environment's --resolve-versions
	// flag: where to look up Rancher/Kubernetes/cert-manager versions and
	// which env-var overrides always win.
	VersionResolution VersionResolutionConfig `json:"version_resolution"`
}

// VersionResolutionConfig configures --resolve-versions on plan-environment.
// See internal/agenticqa/envversions for the resolution algorithm.
type VersionResolutionConfig struct {
	// KDMBaseRawURL is the raw-content base for rancher/kontainer-driver-metadata,
	// e.g. "https://raw.githubusercontent.com/rancher/kontainer-driver-metadata".
	// Defaults to the upstream rancher org when empty.
	KDMBaseRawURL string `json:"kdm_base_raw_url,omitempty"`
	// KDMReleasesURL is the base URL for the packaged KDM data.json served by
	// releases.rancher.com (the source Rancher's image build uses), tried as a
	// fallback when KDMBaseRawURL fails. Defaults to
	// "https://releases.rancher.com/kontainer-driver-metadata" when empty.
	KDMReleasesURL string `json:"kdm_releases_url,omitempty"`
	// DefaultMinor is the Rancher "major.minor" (e.g. "2.15") used as a last
	// resort when neither package/Dockerfile nor the PR base ref yields one.
	DefaultMinor string `json:"default_minor,omitempty"`
	// RancherRawBaseURL is the raw-content base for rancher/rancher, used to
	// read chart/Chart.yaml's kubeVersion constraint. Defaults to the
	// upstream rancher org when empty.
	RancherRawBaseURL string `json:"rancher_raw_base_url,omitempty"`
	// CertManagerReleasesURL is the GitHub API releases-list URL for
	// cert-manager/cert-manager, used as the terminal cert-manager fallback.
	// Defaults to the upstream cert-manager org when empty.
	CertManagerReleasesURL string `json:"cert_manager_releases_url,omitempty"`
	// Overrides maps an env-var name (e.g. "RANCHER_VERSION", "RKE2_VERSION",
	// "K3S_VERSION", "CERT_MANAGER_VERSION", "RANCHER_IMAGE_TAG") to a value
	// that always wins over any resolved value. The actual OS environment
	// variable of the same name, when set, takes precedence over this
	// config-file default.
	Overrides map[string]string `json:"overrides,omitempty"`
	// IncludePrereleasesDefault is the default for --include-prereleases when
	// the flag is not explicitly passed.
	IncludePrereleasesDefault bool `json:"include_prereleases_default,omitempty"`
}

// ChartSizingConfig configures resolution of Helm-chart resource footprints.
type ChartSizingConfig struct {
	// Source describes where to read authoritative chart resource annotations.
	Source ChartSource `json:"source"`
	// Catalog is the curated fallback footprint per chart name, used when the
	// live source lacks the requests annotation (many charts do) or is
	// unavailable. Keys are canonical chart names (e.g. "rancher-monitoring").
	Catalog map[string]ChartFootprintSpec `json:"catalog,omitempty"`
}

// ChartSource locates the Rancher charts repository for live footprint lookup.
type ChartSource struct {
	// LocalPath, when set (or auto-detected), points at a local checkout of
	// rancher/charts. Preferred over network access for speed/determinism.
	LocalPath string `json:"local_path,omitempty"`
	// BaseRawURL is the raw-content base for the rancher/charts repo used when
	// no local checkout is available, e.g.
	// "https://raw.githubusercontent.com/rancher/charts". The release branch is
	// appended per the target Rancher version.
	BaseRawURL string `json:"base_raw_url,omitempty"`
	// DefaultRancherMinor is the Rancher "major.minor" (e.g. "2.14") assumed
	// when the upstream Rancher version is an unresolved ${VAR}. Determines the
	// release-vX.Y branch and which chart versions are considered.
	DefaultRancherMinor string `json:"default_rancher_minor,omitempty"`
}

// ChartFootprintSpec is a curated chart resource footprint (request totals).
type ChartFootprintSpec struct {
	CPUMillis int `json:"cpu_millis"`
	MemoryMiB int `json:"memory_mib"`
	DiskGiB   int `json:"disk_gib,omitempty"`
}

// SizingPolicyConfig is the set of named sizing profiles and the default one to
// apply when --sizing-profile is not supplied.
type SizingPolicyConfig struct {
	// DefaultProfile is the profile name used when no --sizing-profile is given.
	DefaultProfile string `json:"default_profile"`
	// Profiles maps a profile name to its upstream/downstream sub-specs.
	Profiles map[string]SizingProfile `json:"profiles"`
}

// SizingProfile carries separate sizing sub-specs for the upstream (Rancher
// management) and downstream (test) clusters so a profile can, for example,
// apply HA to only one of them.
type SizingProfile struct {
	Upstream   SizingTargetSpec `json:"upstream"`
	Downstream SizingTargetSpec `json:"downstream"`
}

// SizingTargetSpec defines the floors and caps applied to one cluster target.
// Floors raise node counts to a minimum; caps clamp resources/totals. A zero
// value for any field means "no constraint".
type SizingTargetSpec struct {
	// MinEtcd/MinControlPlane/MinWorker raise the quantity of a pool bearing the
	// corresponding role to at least this value.
	MinEtcd         int `json:"min_etcd,omitempty"`
	MinControlPlane int `json:"min_controlplane,omitempty"`
	MinWorker       int `json:"min_worker,omitempty"`
	// MinAllRoles raises the quantity of an all-roles (etcd+controlplane+worker)
	// pool to at least this value. This is how HA is expressed for clusters that
	// use a single combined-role pool.
	MinAllRoles int `json:"min_all_roles,omitempty"`
	// EnforceOddEtcd rounds any etcd-bearing pool's quantity up to the next odd
	// number so etcd retains quorum.
	EnforceOddEtcd bool `json:"enforce_odd_etcd,omitempty"`
	// MaxTotalNodes caps the total node count. HA floors win over this cap (the
	// command warns when floors exceed it).
	MaxTotalNodes int `json:"max_total_nodes,omitempty"`
	// MaxNodeVCPUs/MaxNodeMemoryGiB/MaxNodeDiskGiB clamp per-node recommended
	// specs (only effective with --recommend-specs).
	MaxNodeVCPUs     int `json:"max_node_vcpus,omitempty"`
	MaxNodeMemoryGiB int `json:"max_node_memory_gib,omitempty"`
	MaxNodeDiskGiB   int `json:"max_node_disk_gib,omitempty"`
	// PreferredFamilies is an ordered list of instance-family prefixes the
	// instance-type selector should prefer for this target (e.g. ["t3a","t3"]
	// for cost-conscious profiles, ["m5","c5","r5"] for "performance"). The
	// selector picks the cheapest fitting type from the first family that has a
	// fit; if none fit it falls back to any family. Empty means "any family,
	// cheapest first". Only effective with --recommend-specs.
	PreferredFamilies []string `json:"preferred_families,omitempty"`
}

// UpstreamConfig holds the settings for the upstream Rancher management cluster
// recommendation. The baseline topology is fixed (one all-roles node) and then
// shaped by the sizing policy; these fields supply the distro/version/provider
// and the non-topology values rendered into qa-infra-automation input files.
type UpstreamConfig struct {
	// Provider is the cloud/node provider for the management cluster (e.g.
	// "aws"). Drives the terraform.tfvars module path.
	Provider string `json:"provider"`
	// Env is the qa-infra deployment environment ("default", "airgap", ...).
	// Drives the ansible vars.yaml path.
	Env string `json:"env"`
	// KubernetesDistro is "rke2" or "k3s" for the management cluster.
	KubernetesDistro string `json:"kubernetes_distro"`
	// KubernetesVersion is rendered into the ansible cluster vars.yaml; may be a
	// ${VAR} placeholder.
	KubernetesVersion string `json:"kubernetes_version"`
	// CNI is rendered into the ansible cluster vars.yaml.
	CNI string `json:"cni"`
	// RancherVersion/RancherImageTag/CertManagerVersion/FQDN are rendered into
	// the ansible rancher vars.yaml; values may be ${VAR} placeholders.
	RancherVersion     string `json:"rancher_version"`
	RancherImageTag    string `json:"rancher_image_tag"`
	CertManagerVersion string `json:"cert_manager_version"`
	FQDN               string `json:"fqdn"`
	// RancherChartRepo/RancherChartRepoURL override the Helm repo
	// (name/URL) qa-infra-automation's rancher-playbook.yml adds and installs
	// from, for Rancher versions not published to its default "rancher-latest"
	// channel (e.g. alpha prereleases, which live only in "rancher-alpha").
	// Left empty to use the playbook's built-in default.
	RancherChartRepo    string `json:"rancher_chart_repo,omitempty"`
	RancherChartRepoURL string `json:"rancher_chart_repo_url,omitempty"`
	// BaselineVolumeSizeGiB is the default per-node volume size for the upstream
	// nodes when no recommended spec overrides it.
	BaselineVolumeSizeGiB int `json:"baseline_volume_size_gib"`
	// TfvarsTemplate holds the non-topology terraform.tfvars fields (provider
	// credentials/networking + global instance_type). Values are emitted
	// verbatim, typically ${VAR} placeholders for an envsubst step.
	TfvarsTemplate map[string]string `json:"tfvars_template"`
}

// CattleConfigTemplate describes the non-test-derived sections of a
// cattle-config.yaml: the Rancher server connection, cloud credentials,
// provider machine configs, registries and SSH. Every string value is written
// to the YAML verbatim, so ${VAR} placeholders survive to be expanded by an
// envsubst step before the test runs.
type CattleConfigTemplate struct {
	// Rancher is the "rancher" top-level block (host/adminToken/adminPassword/
	// clusterName/cleanup/insecure). String values are emitted verbatim.
	Rancher map[string]string `json:"rancher,omitempty"`

	// RegistryInput is the "registryInput" block (name/username/password).
	RegistryInput map[string]string `json:"registry_input,omitempty"`

	// SSHPath is the value for the "sshPath.sshPath" field.
	SSHPath string `json:"ssh_path,omitempty"`

	// Credentials maps a provider name (aws/azure/do/harvester/linode/google/
	// vsphere) to its cloud-credential block. The block is emitted under the
	// provider's credential key (see CredentialKeyForProvider).
	Credentials map[string]map[string]string `json:"credentials,omitempty"`

	// MachineConfigs maps a provider name to its machine-config template.
	// Emitted under the provider's machineConfigs key (e.g. awsMachineConfigs).
	MachineConfigs map[string]ProviderMachineConfig `json:"machine_configs,omitempty"`

	// EC2Config is the "awsEC2Configs" block used for custom/ec2 node
	// provisioning. Optional; emitted only when present and the provider is aws.
	EC2Config *EC2ConfigTemplate `json:"ec2_config,omitempty"`

	// ClusterRegistries, when set, is emitted under clusterConfig.registries
	// (e.g. private registry mirrors/auth). Free-form nested structure.
	ClusterRegistries map[string]any `json:"cluster_registries,omitempty"`

	// KubernetesVersionEnvByDistro maps a distro (rke2/k3s/rke1) to the env-var
	// placeholder used for its Kubernetes version when a test does not pin one
	// (e.g. {"rke2": "${RKE2_VERSION}", "k3s": "${K3S_VERSION}"}).
	KubernetesVersionEnvByDistro map[string]string `json:"kubernetes_version_env_by_distro,omitempty"`

	// DefaultNodeProviderByProvider maps a cloud provider to its default
	// nodeProvider value (e.g. {"aws": "ec2"}).
	DefaultNodeProviderByProvider map[string]string `json:"default_node_provider_by_provider,omitempty"`

	// DefaultProvider is the provider to assume when a test group does not pin
	// one (e.g. "aws"). Drives which credential/machine-config blocks are
	// emitted.
	DefaultProvider string `json:"default_provider,omitempty"`

	// InstanceTypeCatalog lists the candidate provider instance types that
	// plan-environment --recommend-specs may select from, keyed by provider
	// (e.g. "aws"). When selecting, the smallest type whose VCPUs and MemoryGiB
	// meet the recommended spec is chosen. Empty for providers that take raw
	// cpu/memory/disk fields (harvester/vsphere).
	InstanceTypeCatalog map[string][]InstanceType `json:"instance_type_catalog,omitempty"`
}

// InstanceType is one selectable provider instance type and its resources, used
// by plan-environment --recommend-specs to map an abstract MachineSpec onto a
// concrete provider instanceType.
type InstanceType struct {
	Name      string `json:"name"`
	VCPUs     int    `json:"vcpus"`
	MemoryGiB int    `json:"memory_gib"`
	// Family is the instance family prefix (e.g. "t3a", "m5"). Used by sizing
	// profiles to express a preference for cheaper general-purpose families
	// (t3a/t3) by default and compute/memory-optimised families (m5/c5/r5) for
	// the "performance" profile. Empty in legacy catalogs (selection then falls
	// back to cost/size ordering).
	Family string `json:"family,omitempty"`
	// CostRank is a relative hourly-cost rank (lower = cheaper) used to break
	// ties between instance types of equal size. It is a relative ordinal, not
	// a real price. Zero in legacy catalogs (treated as "unranked").
	CostRank int `json:"cost_rank,omitempty"`
}

// ProviderMachineConfig is a provider's machineConfigs block. OuterFields are
// emitted at the machineConfigs level (e.g. region for aws), while MachineEntry
// is emitted as the single element of the inner per-machine list. The inner
// list key differs per provider (awsMachineConfig, azureMachineConfig, ...) and
// is resolved by MachineListKeyForProvider.
type ProviderMachineConfig struct {
	OuterFields  map[string]string `json:"outer_fields,omitempty"`
	MachineEntry map[string]string `json:"machine_entry,omitempty"`
}

// EC2ConfigTemplate is the "awsEC2Configs" block. OuterFields are top-level
// (region/awsAccessKeyID/awsSecretAccessKey) and Entry is the single element of
// the awsEC2Config list.
type EC2ConfigTemplate struct {
	OuterFields map[string]string `json:"outer_fields,omitempty"`
	Entry       map[string]string `json:"entry,omitempty"`
}

// credentialKeyForProvider maps a provider name to its cloud-credential YAML
// key (the ConfigurationFileKey used by shepherd's cloudcredentials package).
var credentialKeyForProvider = map[string]string{
	types.ProviderAWS:          "awsCredentials",
	types.ProviderAzure:        "azureCredentials",
	types.ProviderDO:           "digitalOceanCredentials",
	types.ProviderDigitalOcean: "digitalOceanCredentials",
	types.ProviderLinode:       "linodeCredentials",
	types.ProviderHarvester:    "harvesterCredentials",
	types.ProviderGoogle:       "googleCredentials",
	types.ProviderVsphere:      "vmwarevsphereCredentials",
	types.ProviderVsphereCloud: "vmwarevsphereCredentials",
}

// machineConfigsKeyForProvider maps a provider name to its machineConfigs
// top-level YAML key.
var machineConfigsKeyForProvider = map[string]string{
	types.ProviderAWS:          "awsMachineConfigs",
	types.ProviderAzure:        "azureMachineConfigs",
	types.ProviderDO:           "doMachineConfigs",
	types.ProviderDigitalOcean: "doMachineConfigs",
	types.ProviderLinode:       "linodeMachineConfigs",
	types.ProviderHarvester:    "harvesterMachineConfigs",
	types.ProviderVsphere:      "vmwarevsphereMachineConfigs",
	types.ProviderVsphereCloud: "vmwarevsphereMachineConfigs",
}

// machineListKeyForProvider maps a provider name to the inner per-machine list
// key inside its machineConfigs block.
var machineListKeyForProvider = map[string]string{
	types.ProviderAWS:          "awsMachineConfig",
	types.ProviderAzure:        "azureMachineConfig",
	types.ProviderDO:           "doMachineConfig",
	types.ProviderDigitalOcean: "doMachineConfig",
	types.ProviderLinode:       "linodeMachineConfig",
	types.ProviderHarvester:    "harvesterMachineConfig",
	types.ProviderVsphere:      "vmwarevsphereMachineConfig",
	types.ProviderVsphereCloud: "vmwarevsphereMachineConfig",
}

// CredentialKeyForProvider returns the cloud-credential YAML key for a provider,
// or "" if unknown.
func CredentialKeyForProvider(provider string) string {
	return credentialKeyForProvider[strings.ToLower(provider)]
}

// MachineConfigsKeyForProvider returns the machineConfigs YAML key for a
// provider, or "" if unknown.
func MachineConfigsKeyForProvider(provider string) string {
	return machineConfigsKeyForProvider[strings.ToLower(provider)]
}

// MachineListKeyForProvider returns the inner per-machine list key for a
// provider, or "" if unknown.
func MachineListKeyForProvider(provider string) string {
	return machineListKeyForProvider[strings.ToLower(provider)]
}

// SelectInstanceType returns the smallest catalog instance type for the given
// provider whose VCPUs and MemoryGiB both meet the requested minimums, with no
// family preference. Kept for backward compatibility; new callers should use
// SelectInstanceTypeForSpec to honour a sizing profile's PreferredFamilies.
func (t CattleConfigTemplate) SelectInstanceType(provider string, minVCPUs, minMemoryGiB int) (InstanceType, bool) {
	return t.SelectInstanceTypeForSpec(provider, minVCPUs, minMemoryGiB, nil)
}

// SelectInstanceTypeForSpec returns the cheapest catalog instance type for the
// given provider whose VCPUs and MemoryGiB both meet the requested minimums,
// honouring an ordered list of preferred instance families.
//
// Selection algorithm:
//  1. Filter the catalog to entries meeting the minimums.
//  2. If preferredFamilies is non-empty, walk the families in order and pick
//     the first family tier that has at least one fitting entry; selection is
//     then restricted to that family. If none of the preferred families fit,
//     fall back to all fitting entries (any family) so a plan is still produced.
//  3. Within the chosen set, rank by (CostRank, vCPUs, memoryGiB, name) so the
//     cheapest sufficient type wins, with deterministic tie-breaks.
//
// Returns ("", false) when the provider has no catalog or no entry satisfies
// the request; the caller should then fall back to the template placeholder.
func (t CattleConfigTemplate) SelectInstanceTypeForSpec(provider string, minVCPUs, minMemoryGiB int, preferredFamilies []string) (InstanceType, bool) {
	catalog := t.InstanceTypeCatalog[strings.ToLower(provider)]
	if len(catalog) == 0 {
		return InstanceType{}, false
	}

	// Collect all entries that meet the resource minimums.
	var fitting []InstanceType
	for _, it := range catalog {
		if it.VCPUs >= minVCPUs && it.MemoryGiB >= minMemoryGiB {
			fitting = append(fitting, it)
		}
	}
	if len(fitting) == 0 {
		return InstanceType{}, false
	}

	// Restrict to the first preferred family that has a fitting entry.
	candidates := fitting
	for _, fam := range preferredFamilies {
		fam = strings.ToLower(strings.TrimSpace(fam))
		if fam == "" {
			continue
		}
		var inFamily []InstanceType
		for _, it := range fitting {
			if strings.ToLower(it.Family) == fam {
				inFamily = append(inFamily, it)
			}
		}
		if len(inFamily) > 0 {
			candidates = inFamily
			break
		}
	}

	best := candidates[0]
	for _, it := range candidates[1:] {
		if lessInstanceType(it, best) {
			best = it
		}
	}
	return best, true
}

// lessInstanceType ranks instance types by cost (CostRank, lower=cheaper), then
// vCPUs, then memory, then name so selection is deterministic and picks the
// cheapest sufficient type. A zero CostRank (legacy/unranked) sorts after
// ranked entries so explicitly-ranked cheap types are preferred.
func lessInstanceType(a, b InstanceType) bool {
	ra, rb := costRankOrInf(a.CostRank), costRankOrInf(b.CostRank)
	if ra != rb {
		return ra < rb
	}
	if a.VCPUs != b.VCPUs {
		return a.VCPUs < b.VCPUs
	}
	if a.MemoryGiB != b.MemoryGiB {
		return a.MemoryGiB < b.MemoryGiB
	}
	return a.Name < b.Name
}

// costRankOrInf maps an unranked (zero) CostRank to a large sentinel so ranked
// entries sort ahead of unranked ones.
func costRankOrInf(rank int) int {
	if rank <= 0 {
		return 1 << 30
	}
	return rank
}

// IsEmpty reports whether the cattle-config template carries no usable content.
// This happens when a pipeline_env.json predates the cattle_config section (it
// was generated by an older agentic-qa) or when the operator deliberately
// omitted it. Callers should fall back to GenerateCattleConfigTemplate() and
// warn, otherwise plan-environment would emit a config missing the rancher,
// credentials and machine-config sections.
func (t CattleConfigTemplate) IsEmpty() bool {
	return len(t.Rancher) == 0 &&
		len(t.RegistryInput) == 0 &&
		t.SSHPath == "" &&
		len(t.Credentials) == 0 &&
		len(t.MachineConfigs) == 0 &&
		t.EC2Config == nil &&
		len(t.ClusterRegistries) == 0 &&
		len(t.KubernetesVersionEnvByDistro) == 0 &&
		t.DefaultProvider == ""
}

// GenerateCattleConfigTemplate returns the default (Rancher CI) cattle-config
// template. Exposed so callers can fall back to it when a loaded
// pipeline_env.json omits the cattle_config section.
func GenerateCattleConfigTemplate() CattleConfigTemplate {
	return generateCattleConfigTemplate()
}

// JobNameTagPattern maps a Jenkins job name substring pattern to the build tag
// that should be applied when the pattern matches.
type JobNameTagPattern struct {
	// Pattern is the substring to search for in the lowercase job name.
	Pattern string `json:"pattern"`
	// Tag is the build tag to emit when Pattern matches.
	Tag string `json:"tag"`
	// ExcludePattern, if set, suppresses the match when the job name also
	// contains this substring (e.g. exclude "pit" when matching "daily").
	ExcludePattern string `json:"exclude_pattern,omitempty"`
}

// Validate checks that required fields are populated.
func (e *PipelineEnv) Validate() error {
	if e.QaseATNFieldID <= 0 {
		return fmt.Errorf("pipeline_env: qase_atn_field_id must be a positive integer")
	}
	if len(e.QaseProjects) == 0 {
		return fmt.Errorf("pipeline_env: qase_projects must not be empty")
	}
	if e.ProductRepo == "" {
		return fmt.Errorf("pipeline_env: product_repo must not be empty")
	}
	if e.TestsRepo == "" {
		return fmt.Errorf("pipeline_env: tests_repo must not be empty")
	}
	if e.DefaultQaseProject == "" {
		return fmt.Errorf("pipeline_env: default_qase_project must not be empty")
	}
	if e.CopilotUsername == "" {
		return fmt.Errorf("pipeline_env: copilot_username must not be empty")
	}
	if e.AgenticQALabel == "" {
		return fmt.Errorf("pipeline_env: agentic_qa_label must not be empty")
	}
	if e.ProjectDisplayName == "" {
		return fmt.Errorf("pipeline_env: project_display_name must not be empty")
	}
	// The sizing policy is optional in the file (plan-environment falls back to
	// built-in defaults when absent), but when present it must be coherent.
	if !e.SizingPolicy.IsEmpty() {
		if err := e.SizingPolicy.validate(); err != nil {
			return err
		}
	}
	return nil
}

// validate checks that the sizing policy is internally consistent: the default
// profile must exist and all spec fields must be non-negative.
func (s SizingPolicyConfig) validate() error {
	if s.DefaultProfile == "" {
		return fmt.Errorf("pipeline_env: sizing_policy.default_profile must not be empty")
	}
	if _, ok := s.Profiles[s.DefaultProfile]; !ok {
		return fmt.Errorf("pipeline_env: sizing_policy.default_profile %q is not defined in profiles", s.DefaultProfile)
	}
	for name, p := range s.Profiles {
		for target, spec := range map[string]SizingTargetSpec{
			types.SizingTargetUpstream:   p.Upstream,
			types.SizingTargetDownstream: p.Downstream,
		} {
			if err := spec.validate(); err != nil {
				return fmt.Errorf("pipeline_env: sizing_policy.profiles[%q].%s: %w", name, target, err)
			}
		}
	}
	return nil
}

// validate ensures a target spec has no negative values.
func (t SizingTargetSpec) validate() error {
	for field, v := range map[string]int{
		"min_etcd":            t.MinEtcd,
		"min_controlplane":    t.MinControlPlane,
		"min_worker":          t.MinWorker,
		"min_all_roles":       t.MinAllRoles,
		"max_total_nodes":     t.MaxTotalNodes,
		"max_node_vcpus":      t.MaxNodeVCPUs,
		"max_node_memory_gib": t.MaxNodeMemoryGiB,
		"max_node_disk_gib":   t.MaxNodeDiskGiB,
	} {
		if v < 0 {
			return fmt.Errorf("%s must not be negative", field)
		}
	}
	return nil
}

// Load reads and parses a pipeline_env.json file from the given path.
// Returns an error if the file cannot be read, is malformed, or fails
// validation.
func Load(path string) (*PipelineEnv, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading pipeline_env file %q: %w", path, err)
	}
	var env PipelineEnv
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parsing pipeline_env file %q: %w", path, err)
	}
	if err := env.Validate(); err != nil {
		return nil, err
	}
	return &env, nil
}

// Generate returns a PipelineEnv pre-populated with the Rancher/SUSE
// defaults that were previously compiled into the binary.  Operators can
// marshal this to JSON, save it as pipeline_env.json, and customise as
// needed.
func Generate() *PipelineEnv {
	return &PipelineEnv{
		QaseATNFieldID: qase.AutomationTestNameFieldID,
		QaseProjects:   []string{"RANCHERINT", "RRT", "RM", "K3SRKE2"},
		QaseProjectNames: map[string]string{
			"RANCHERINT": "Rancher Integration Tests",
			"RRT":        "Rancher Regression Tests",
			"RM":         "Rancher Manual Tests",
			"K3SRKE2":    "K3s/RKE2 Tests",
		},
		TagToJob: map[string]string{
			"pit.daily":           "go-pit-daily-individual-job-updated",
			"pit.weekly":          "go-pit-weekly-multibranch-job",
			"pit.harvester.daily": "harvester-e2e-recurring-job",
			"pit.elemental":       "go-pit-daily-individual-job-updated",
			"pit.event":           "go-pit-daily-individual-job-updated",
			"sanity":              "go-recurring-daily-individual-job",
			"extended":            "go-recurring-weekly-individual-job",
			"stress":              "go-recurring-biweekly-individual-job",
			"validation":          "go-automation-freeform-job",
			"recurring":           "go-recurring-daily-individual-job",
		},
		TagToQaseProject: map[string]string{
			// Prefix "pit." covers pit.daily, pit.weekly, pit.harvester.daily, etc.
			"pit.":       "RANCHERINT",
			"sanity":     "RANCHERINT",
			"extended":   "RANCHERINT",
			"stress":     "RANCHERINT",
			"validation": "RANCHERINT",
			"recurring":  "RANCHERINT",
		},
		JobNamePatternToQaseProject: map[string]string{
			"harvester": "RANCHERINT",
			"tfp-":      "RANCHERINT",
			"airgap":    "RANCHERINT",
		},
		// JobNameTagPatterns encodes the Rancher Jenkins naming conventions.
		// Each rule is tried in order; ExcludePattern prevents false positives.
		JobNameTagPatterns: []JobNameTagPattern{
			{Pattern: "pit-daily", Tag: "pit.daily"},
			{Pattern: "pit.daily", Tag: "pit.daily"},
			{Pattern: "pit-weekly", Tag: "pit.weekly"},
			{Pattern: "pit.weekly", Tag: "pit.weekly"},
			{Pattern: "pit-harvester", Tag: "pit.harvester.daily"},
			{Pattern: "pit.harvester", Tag: "pit.harvester.daily"},
			{Pattern: "pit-elemental", Tag: "pit.elemental"},
			{Pattern: "pit.elemental", Tag: "pit.elemental"},
			{Pattern: "biweekly", Tag: "stress"},
			{Pattern: "recurring-weekly", Tag: "extended"},
			{Pattern: "weekly-individual", Tag: "extended"},
			{Pattern: "recurring-daily", Tag: "sanity", ExcludePattern: "pit"},
			{Pattern: "daily-individual", Tag: "sanity", ExcludePattern: "pit"},
			{Pattern: "sanity", Tag: "sanity"},
			{Pattern: "freeform", Tag: "validation"},
			{Pattern: "airgap", Tag: "airgap"},
			{Pattern: "harvester", Tag: "harvester", ExcludePattern: "pit"},
		},
		DefaultQaseProject: "RANCHERINT",
		ProductRepo:        "rancher/rancher",
		TestsRepo:          "rancher/tests",
		CopilotUsername:    "copilot-swe-agent",
		AgenticQALabel:     "agentic-qa",
		ProjectDisplayName: "Rancher Kubernetes management platform",
		CattleConfig:       generateCattleConfigTemplate(),
		SizingPolicy:       generateSizingPolicy(),
		Upstream:           generateUpstreamConfig(),
		ChartSizing:        generateChartSizing(),
		VersionResolution:  generateVersionResolution(),
	}
}

// generateVersionResolution returns the default --resolve-versions settings:
// public upstream sources, no overrides, prereleases excluded by default.
func generateVersionResolution() VersionResolutionConfig {
	return VersionResolutionConfig{
		KDMBaseRawURL:             "https://raw.githubusercontent.com/rancher/kontainer-driver-metadata",
		KDMReleasesURL:            "https://releases.rancher.com/kontainer-driver-metadata",
		RancherRawBaseURL:         "https://raw.githubusercontent.com/rancher/rancher",
		CertManagerReleasesURL:    "https://api.github.com/repos/cert-manager/cert-manager/releases",
		IncludePrereleasesDefault: false,
	}
}

// generateSizingPolicy returns the built-in sizing profiles. "minimal" is a
// no-op (preserving the historical minimum-viable behaviour) and is the
// default; "balanced" guarantees at least one all-roles node; "ha" applies
// high-availability floors to both upstream and downstream clusters.
//
// NOTE: the default profile is intentionally "minimal" to preserve existing
// behaviour. Operators running real (non-dev) pipelines should consider
// switching DefaultProfile to "balanced" or "ha".
func generateSizingPolicy() SizingPolicyConfig {
	return SizingPolicyConfig{
		DefaultProfile: types.SizingProfileMinimal,
		Profiles: map[string]SizingProfile{
			types.SizingProfileMinimal: {
				// No floors/caps — minimum viable as derived. Cheapest families.
				Upstream:   SizingTargetSpec{PreferredFamilies: types.CostConsciousFamilies},
				Downstream: SizingTargetSpec{PreferredFamilies: types.CostConsciousFamilies},
			},
			types.SizingProfileBalanced: {
				Upstream:   SizingTargetSpec{MinAllRoles: 1, PreferredFamilies: types.CostConsciousFamilies},
				Downstream: SizingTargetSpec{MinAllRoles: 1, MinWorker: 1, PreferredFamilies: types.CostConsciousFamilies},
			},
			types.SizingProfileHA: {
				Upstream: SizingTargetSpec{
					MinAllRoles:       3,
					EnforceOddEtcd:    true,
					PreferredFamilies: types.CostConsciousFamilies,
				},
				Downstream: SizingTargetSpec{
					MinEtcd:           3,
					MinControlPlane:   2,
					MinWorker:         2,
					MinAllRoles:       3,
					EnforceOddEtcd:    true,
					PreferredFamilies: types.CostConsciousFamilies,
				},
			},
			types.SizingProfilePerformance: {
				// High-load suites: prefer non-burstable performance families and
				// allow larger per-node sizes. Modest worker floor so perf suites
				// aren't single-worker. HA-style etcd is left to the ha profile.
				Upstream: SizingTargetSpec{
					MinAllRoles:       1,
					PreferredFamilies: types.PerformanceFamilies,
					MaxNodeVCPUs:      16,
					MaxNodeMemoryGiB:  64,
				},
				Downstream: SizingTargetSpec{
					MinAllRoles:       1,
					MinWorker:         2,
					PreferredFamilies: types.PerformanceFamilies,
					MaxNodeVCPUs:      16,
					MaxNodeMemoryGiB:  64,
				},
			},
		},
	}
}

// generateUpstreamConfig returns the default upstream (Rancher management)
// cluster configuration. Sensitive/site-specific values are ${VAR}
// placeholders for an envsubst step. Field/key names mirror the
// qa-infra-automation terraform.tfvars (AWS cluster_nodes module) and ansible
// vars.yaml schemas.
func generateUpstreamConfig() UpstreamConfig {
	return UpstreamConfig{
		Provider:              types.ProviderAWS,
		Env:                   "default",
		KubernetesDistro:      types.DistroRKE2,
		KubernetesVersion:     "${RKE2_VERSION}",
		CNI:                   types.CNICalico,
		RancherVersion:        "${RANCHER_VERSION}",
		RancherImageTag:       "${RANCHER_IMAGE_TAG}",
		CertManagerVersion:    "${CERT_MANAGER_VERSION}",
		FQDN:                  "${RANCHER_FQDN}",
		BaselineVolumeSizeGiB: 40,
		TfvarsTemplate: map[string]string{
			"aws_access_key":      "${AWS_ACCESS_KEY}",
			"aws_secret_key":      "${AWS_SECRET_KEY}",
			"aws_region":          "${AWS_REGION}",
			"aws_ami":             "${AWS_AMI}",
			"aws_ssh_user":        "${AWS_USER}",
			"aws_vpc":             "${AWS_VPC_ID}",
			"aws_subnet":          "${AWS_SUBNET_ID}",
			"aws_security_group":  "${AWS_SECURITY_GROUPS}",
			"aws_route53_zone":    "${AWS_ROUTE53_ZONE}",
			"aws_hostname_prefix": "${AWS_HOSTNAME_PREFIX}",
			"aws_volume_type":     "${AWS_VOLUME_TYPE}",
			"public_ssh_key":      "${PUBLIC_SSH_KEY_PATH}",
			"private_ssh_key":     "${SSH_PRIVATE_KEY_PATH}",
			"instance_type":       "${AWS_INSTANCE_TYPE}",
		},
	}
}

// GenerateSizingPolicy returns the default sizing policy. Exposed so callers can
// fall back to it when a loaded pipeline_env.json omits the sizing_policy
// section.
func GenerateSizingPolicy() SizingPolicyConfig {
	return generateSizingPolicy()
}

// generateChartSizing returns the default chart-sizing config: a live source
// (local checkout preferred, else rancher/charts raw GitHub) plus a curated
// fallback catalog for the heavy charts whose Chart.yaml lacks the
// catalog.cattle.io/requests-* annotations. Footprints are conservative
// request totals (over-estimating is safe for sizing).
func generateChartSizing() ChartSizingConfig {
	return ChartSizingConfig{
		Source: ChartSource{
			BaseRawURL:          "https://raw.githubusercontent.com/rancher/charts",
			DefaultRancherMinor: "2.14",
		},
		Catalog: map[string]ChartFootprintSpec{
			// Annotated upstream too, but included for offline use.
			"rancher-monitoring": {CPUMillis: 4500, MemoryMiB: 4000, DiskGiB: 50},
			"rancher-istio":      {CPUMillis: 710, MemoryMiB: 2314, DiskGiB: 10},
			"rancher-logging":    {CPUMillis: 1000, MemoryMiB: 2048, DiskGiB: 10},
			// Unannotated heavy charts — curated estimates.
			"longhorn":                 {CPUMillis: 1500, MemoryMiB: 2048, DiskGiB: 40},
			"neuvector":                {CPUMillis: 1000, MemoryMiB: 2048, DiskGiB: 10},
			"rancher-cis-benchmark":    {CPUMillis: 500, MemoryMiB: 512, DiskGiB: 5},
			"rancher-compliance":       {CPUMillis: 500, MemoryMiB: 512, DiskGiB: 5},
			"rancher-gatekeeper":       {CPUMillis: 500, MemoryMiB: 512, DiskGiB: 5},
			"rancher-backup":           {CPUMillis: 500, MemoryMiB: 512, DiskGiB: 10},
			"rancher-alerting-drivers": {CPUMillis: 250, MemoryMiB: 256, DiskGiB: 5},
		},
	}
}

// GenerateChartSizing returns the default chart-sizing config. Exposed so
// callers can fall back to it when a loaded pipeline_env.json omits the
// chart_sizing section.
func GenerateChartSizing() ChartSizingConfig {
	return generateChartSizing()
}

// IsEmpty reports whether the chart-sizing config carries no usable content
// (e.g. a pipeline_env.json predating the chart_sizing section).
func (c ChartSizingConfig) IsEmpty() bool {
	return c.Source.BaseRawURL == "" && c.Source.LocalPath == "" && len(c.Catalog) == 0
}

// GenerateUpstreamConfig returns the default upstream config. Exposed so callers
// can fall back to it when a loaded pipeline_env.json omits the upstream
// section.
func GenerateUpstreamConfig() UpstreamConfig {
	return generateUpstreamConfig()
}

// IsEmpty reports whether the sizing policy carries no usable content (e.g. a
// pipeline_env.json predating the sizing_policy section).
func (s SizingPolicyConfig) IsEmpty() bool {
	return s.DefaultProfile == "" && len(s.Profiles) == 0
}

// ResolveProfile returns the named profile, falling back to DefaultProfile when
// name is empty. The bool is false when neither resolves to a known profile.
func (s SizingPolicyConfig) ResolveProfile(name string) (SizingProfile, string, bool) {
	if name == "" {
		name = s.DefaultProfile
	}
	p, ok := s.Profiles[name]
	return p, name, ok
}

// IsEmpty reports whether the upstream config carries no usable content (e.g. a
// pipeline_env.json predating the upstream section).
func (u UpstreamConfig) IsEmpty() bool {
	return u.Provider == "" && u.KubernetesDistro == "" && len(u.TfvarsTemplate) == 0
}

// generateCattleConfigTemplate returns the Rancher CI cattle-config template,
// mirroring .github/scripts/platform-qa-create-cattle-config.sh. All sensitive
// values are ${VAR} placeholders meant to be expanded by an envsubst step at
// pipeline runtime (shepherd does not expand env vars itself).
func generateCattleConfigTemplate() CattleConfigTemplate {
	return CattleConfigTemplate{
		Rancher: map[string]string{
			"host":          "${RANCHER_HOST}",
			"adminToken":    "${RANCHER_ADMIN_TOKEN}",
			"adminPassword": "${RANCHER_ADMIN_PASSWORD}",
			"clusterName":   "${CLUSTER_NAME}",
			"cleanup":       "true",
			"insecure":      "true",
		},
		RegistryInput: map[string]string{
			"name":     "${QUAY_REGISTRY_NAME}",
			"username": "${QUAY_REGISTRY_USERNAME}",
			"password": "${QUAY_REGISTRY_PASSWORD}",
		},
		SSHPath:         "${SSH_PRIVATE_KEY_PATH}",
		DefaultProvider: types.ProviderAWS,
		Credentials: map[string]map[string]string{
			types.ProviderAWS: {
				"accessKey":     "${AWS_ACCESS_KEY}",
				"secretKey":     "${AWS_SECRET_KEY}",
				"defaultRegion": "${AWS_REGION}",
			},
		},
		MachineConfigs: map[string]ProviderMachineConfig{
			types.ProviderAWS: {
				OuterFields: map[string]string{
					"region": "${AWS_REGION}",
				},
				MachineEntry: map[string]string{
					"ami":           "${AWS_AMI}",
					"instanceType":  "${AWS_INSTANCE_TYPE}",
					"sshUser":       "${AWS_USER}",
					"vpcId":         "${AWS_VPC_ID}",
					"volumeType":    "${AWS_VOLUME_TYPE}",
					"zone":          "${AWS_ZONE_LETTER}",
					"retries":       "5",
					"rootSize":      "${AWS_ROOT_SIZE}",
					"securityGroup": "${AWS_SECURITY_GROUP_NAMES}",
				},
			},
		},
		EC2Config: &EC2ConfigTemplate{
			OuterFields: map[string]string{
				"region":             "${AWS_REGION}",
				"awsSecretAccessKey": "${AWS_SECRET_KEY}",
				"awsAccessKeyID":     "${AWS_ACCESS_KEY}",
			},
			Entry: map[string]string{
				"instanceType":       "${AWS_INSTANCE_TYPE}",
				"awsRegionAZ":        "${AWS_REGION}${AWS_ZONE_LETTER}",
				"awsAMI":             "${AWS_AMI}",
				"awsSecurityGroups":  "${AWS_SECURITY_GROUPS}",
				"awsSubnetID":        "${AWS_SUBNET_ID}",
				"awsSSHKeyName":      "${SSH_PRIVATE_KEY_NAME}.pem",
				"awsCICDInstanceTag": defaultAWSCICDInstanceTag,
				"awsIAMProfile":      "${AWS_IAM_PROFILE}",
				"awsUser":            "${AWS_USER}",
				"volumeSize":         "${AWS_ROOT_SIZE}",
			},
		},
		ClusterRegistries: map[string]any{
			"rke2Registries": map[string]any{
				"mirrors": map[string]any{
					"docker.io": map[string]any{
						"endpoint": []any{"https://${QA_PRIVATE_REGISTRY_NAME}"},
					},
				},
				"configs": map[string]any{
					"${QA_PRIVATE_REGISTRY_NAME}": map[string]any{
						"auth": map[string]any{
							"username": "${DOCKERHUB_USERNAME}",
							"password": "${DOCKERHUB_PASSWORD}",
						},
					},
				},
			},
		},
		KubernetesVersionEnvByDistro: map[string]string{
			types.DistroRKE2: "${RKE2_VERSION}",
			types.DistroK3S:  "${K3S_VERSION}",
		},
		DefaultNodeProviderByProvider: map[string]string{
			types.ProviderAWS: "ec2",
		},
		// A small default AWS catalog used by --recommend-specs to map abstract
		// specs to a concrete instanceType. CostRank is a relative ordinal
		// (lower = cheaper): the burstable AMD t3a family is cheapest, then
		// burstable t3, then general-purpose m5, compute-optimised c5, and
		// memory-optimised r5. Sizing profiles steer selection toward families
		// via PreferredFamilies. Operators can extend or restrict this list.
		InstanceTypeCatalog: map[string][]InstanceType{
			types.ProviderAWS: {
				// t3a (AMD burstable) — cheapest general-purpose tier.
				{Name: "t3a.medium", VCPUs: 2, MemoryGiB: 4, Family: "t3a", CostRank: 10},
				{Name: "t3a.large", VCPUs: 2, MemoryGiB: 8, Family: "t3a", CostRank: 11},
				{Name: "t3a.xlarge", VCPUs: 4, MemoryGiB: 16, Family: "t3a", CostRank: 12},
				{Name: "t3a.2xlarge", VCPUs: 8, MemoryGiB: 32, Family: "t3a", CostRank: 13},
				// t3 (Intel burstable) — slightly pricier than t3a.
				{Name: "t3.medium", VCPUs: 2, MemoryGiB: 4, Family: "t3", CostRank: 20},
				{Name: "t3.large", VCPUs: 2, MemoryGiB: 8, Family: "t3", CostRank: 21},
				{Name: "t3.xlarge", VCPUs: 4, MemoryGiB: 16, Family: "t3", CostRank: 22},
				{Name: "t3.2xlarge", VCPUs: 8, MemoryGiB: 32, Family: "t3", CostRank: 23},
				// m5 (general-purpose, non-burstable) — performance baseline.
				{Name: "m5.large", VCPUs: 2, MemoryGiB: 8, Family: "m5", CostRank: 40},
				{Name: "m5.xlarge", VCPUs: 4, MemoryGiB: 16, Family: "m5", CostRank: 41},
				{Name: "m5.2xlarge", VCPUs: 8, MemoryGiB: 32, Family: "m5", CostRank: 42},
				{Name: "m5.4xlarge", VCPUs: 16, MemoryGiB: 64, Family: "m5", CostRank: 43},
				// c5 (compute-optimised).
				{Name: "c5.xlarge", VCPUs: 4, MemoryGiB: 8, Family: "c5", CostRank: 50},
				{Name: "c5.2xlarge", VCPUs: 8, MemoryGiB: 16, Family: "c5", CostRank: 51},
				{Name: "c5.4xlarge", VCPUs: 16, MemoryGiB: 32, Family: "c5", CostRank: 52},
				// r5 (memory-optimised).
				{Name: "r5.xlarge", VCPUs: 4, MemoryGiB: 32, Family: "r5", CostRank: 60},
				{Name: "r5.2xlarge", VCPUs: 8, MemoryGiB: 64, Family: "r5", CostRank: 61},
			},
		},
	}
}

// QaseProjectName returns the human-readable name for a project code,
// falling back to the code itself if not found in QaseProjectNames.
func (e *PipelineEnv) QaseProjectName(code string) string {
	if name, ok := e.QaseProjectNames[code]; ok {
		return name
	}
	return code
}

// InferQaseProject returns the Qase project code for a given set of build
// tags and job name, using TagToQaseProject (full tag, then "prefix.")
// followed by JobNamePatternToQaseProject, then DefaultQaseProject.
func (e *PipelineEnv) InferQaseProject(jobName string, tags []string) string {
	for _, tag := range tags {
		// Exact match first.
		if proj, ok := e.TagToQaseProject[tag]; ok {
			return proj
		}
		// Prefix match: try progressively shorter dot-delimited prefixes.
		for i := len(tag); i > 0; i-- {
			if tag[i-1] == '.' {
				prefix := tag[:i] // e.g. "pit."
				if proj, ok := e.TagToQaseProject[prefix]; ok {
					return proj
				}
			}
		}
	}
	// Job-name substring match.
	for pattern, proj := range e.JobNamePatternToQaseProject {
		if containsFold(jobName, pattern) {
			return proj
		}
	}
	return e.DefaultQaseProject
}

// InferTagsFromJobName applies JobNameTagPatterns to derive build tags from a
// Jenkins job name.  Matches are case-insensitive.
func (e *PipelineEnv) InferTagsFromJobName(name string) []string {
	lower := strings.ToLower(name)
	seen := map[string]struct{}{}
	var tags []string

	for _, rule := range e.JobNameTagPatterns {
		if rule.Pattern == "" || rule.Tag == "" {
			continue
		}
		if !strings.Contains(lower, strings.ToLower(rule.Pattern)) {
			continue
		}
		if rule.ExcludePattern != "" && strings.Contains(lower, strings.ToLower(rule.ExcludePattern)) {
			continue
		}
		if _, dup := seen[rule.Tag]; !dup {
			seen[rule.Tag] = struct{}{}
			tags = append(tags, rule.Tag)
		}
	}
	return tags
}

// RepoForCategory returns the recommended GitHub repository slug for a triage
// classification category.
func (e *PipelineEnv) RepoForCategory(category string) string {
	switch category {
	case string(types.ClassProductDefect):
		return e.ProductRepo
	case string(types.ClassTestDefect):
		return e.TestsRepo
	default:
		return ""
	}
}

// containsFold is a case-insensitive strings.Contains.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
