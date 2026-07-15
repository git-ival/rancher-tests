package types

// IdentifiedTests is the output of the "identify" step.
type IdentifiedTests struct {
	PRNumber int    `json:"pr_number"`
	PRTitle  string `json:"pr_title"`
	PRURL    string `json:"pr_url"`
	// Repo is the "owner/repo" slug the PR was fetched from. Persisted so
	// downstream steps (e.g. plan-environment --resolve-versions) can resolve
	// GitHub state without requiring the repo to be re-specified.
	Repo string `json:"repo,omitempty"`
	// Only meaningful once Merged is true or the PR is mergeable. Used to find the earliest release tag containing the PR.
	MergeCommitSHA string `json:"merge_commit_sha,omitempty"`
	// Merged reports whether the PR has been merged into its base branch.
	Merged bool `json:"merged"`
	// BaseRef is the PR's base branch (e.g. "release-v2.14"). Used to scope
	// candidate release tags and to locate the in-development fallback branch.
	BaseRef         string      `json:"base_ref,omitempty"`
	FeatureAreas    []string    `json:"feature_areas"`
	ChangedFiles    []string    `json:"changed_files"`
	Tests           []TestEntry `json:"tests"`
	RecommendedTags []string    `json:"recommended_tags"`
	RecommendedJobs []string    `json:"recommended_jobs"`
	Confidence      string      `json:"confidence"`
	// QaseProjects is the de-duplicated, consolidated list of Qase project codes
	// that will each receive one test run. Populated by identify after LLM call.
	QaseProjects []string `json:"qase_projects"`
	// TestsByProject maps each project code to the indices of its assigned tests
	// in the Tests slice. Populated by identify after consolidation.
	TestsByProject map[string][]int `json:"tests_by_project"`
}

// TestEntry describes a single test identified for execution.
type TestEntry struct {
	File         string   `json:"file"`
	Suite        string   `json:"suite,omitempty"`
	Functions    []string `json:"functions,omitempty"`
	BuildTags    []string `json:"build_tags,omitempty"`
	QaseProjects []string `json:"qase_projects,omitempty"`
	QaseCaseIDs  []int    `json:"qase_case_ids,omitempty"`
	// QaseCasesByProject maps each Qase project code to the case IDs that
	// belong to that specific project. This ensures the trigger step only
	// sends project-valid case IDs when creating runs.
	QaseCasesByProject map[string][]int `json:"qase_cases_by_project,omitempty"`
	QaseSchema         string           `json:"qase_schema,omitempty"`
	RelevanceScore     float64          `json:"relevance_score,omitempty"`
	Reasoning          string           `json:"reasoning,omitempty"`
}

// TriggeredQaseRun records a single Qase run created for one project.
type TriggeredQaseRun struct {
	Project string `json:"project"`
	RunID   int    `json:"run_id"`
}

// TriggeredJobs is the output of the "trigger" step.
type TriggeredJobs struct {
	// QaseRuns holds one entry per Qase project that received a run.
	QaseRuns []TriggeredQaseRun `json:"qase_runs"`
	// QaseRunID and QaseProject retain the first run for backward compat
	// with anything consuming the single-run output format.
	QaseRunID   *int           `json:"qase_run_id"`
	QaseProject string         `json:"qase_project"`
	Jobs        []TriggeredJob `json:"jobs"`
	TriggeredAt string         `json:"triggered_at"`
}

// TriggeredJob describes a single Jenkins job that was triggered.
type TriggeredJob struct {
	JobName     string            `json:"job_name"`
	BuildNumber *int              `json:"build_number"`
	QueueID     *int              `json:"queue_id"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	Status      string            `json:"status"`
	TestFile    string            `json:"test_file,omitempty"`
}

// CompletedJobs is the output of the "wait" step.
type CompletedJobs struct {
	// QaseRuns propagates the per-project run list from TriggeredJobs.
	QaseRuns             []TriggeredQaseRun `json:"qase_runs"`
	QaseRunID            *int               `json:"qase_run_id"` // compat
	Completed            []CompletedJob     `json:"completed"`
	Failed               []CompletedJob     `json:"failed"`
	TotalDurationMinutes float64            `json:"total_duration_minutes"`
	CompletedAt          string             `json:"completed_at"`
}

// CompletedJob describes a single Jenkins job that has finished.
type CompletedJob struct {
	JobName         string  `json:"job_name"`
	BuildNumber     *int    `json:"build_number"`
	Status          string  `json:"status"`
	DurationMinutes float64 `json:"duration_minutes"`
	LogURL          string  `json:"log_url,omitempty"`
}

// ---------------------------------------------------------------------------
// Domain constants shared across packages
// ---------------------------------------------------------------------------

// Node role names used in machine-pool configuration and role-signature logic.
const (
	RoleEtcd         = "etcd"
	RoleControlPlane = "controlplane"
	RoleWorker       = "worker"
	RoleWindows      = "windows"
)

// Kubernetes distro identifiers.
const (
	DistroRKE2 = "rke2"
	DistroK3S  = "k3s"
	DistroRKE1 = "rke1"
)

// Cloud / node provider identifiers.
const (
	ProviderAWS          = "aws"
	ProviderAzure        = "azure"
	ProviderDO           = "do"
	ProviderDigitalOcean = "digitalocean"
	ProviderHarvester    = "harvester"
	ProviderLinode       = "linode"
	ProviderGoogle       = "google"
	ProviderVsphere      = "vsphere"
	ProviderVsphereCloud = "rancher-vsphere"
	ProviderExternal     = "external"
)

// CNI plugin identifiers.
const (
	CNICalico  = "calico"
	CNICilium  = "cilium"
	CNICanal   = "canal"
	CNIFlannel = "flannel"
	CNIMultus  = "multus"
	CNIWeave   = "weave"
)

// PSACT profile identifiers.
const (
	PSACTPrivileged = "rancher-privileged"
	PSACTRestricted = "rancher-restricted"
	PSACTBaseline   = "rancher-baseline"
)

// Network stack preference values.
const (
	StackPreferenceDual = "dual"
)

// Workload kind strings used as WorkloadRequirement.Kind values and in
// workload weight tables.
const (
	WorkloadDeployment  = "deployment"
	WorkloadPod         = "pod"
	WorkloadDaemonSet   = "daemonset"
	WorkloadStatefulSet = "statefulset"
	WorkloadCronJob     = "cronjob"
	WorkloadJob         = "job"
	WorkloadIngress     = "ingress"
	WorkloadHPA         = "horizontalpodautoscaler"
)

// Spec source labels recorded in MachineSpec.Source.
const (
	SpecSourceHeuristic = "heuristic"
	SpecSourceLLM       = "llm"
)

// Environment-plan strategy values.
const (
	PlanStrategySingle   = "single"
	PlanStrategyPerGroup = "per_group"
	PlanGroupNameAll     = "all"
)

// Plan derivation method labels recorded in EnvironmentGroup.DerivedBy.
const (
	DerivedByStatic  = "static"
	DerivedByLLM     = "llm"
	DerivedByDefault = "default"
)

// Config-failure action values used by the config-failures command.
const (
	CFActionRerun = "rerun"
	CFActionGuard = "guard"
)

// Sizing profile names used by plan-environment --sizing-profile.
const (
	SizingProfileMinimal     = "minimal"
	SizingProfileBalanced    = "balanced"
	SizingProfileHA          = "ha"
	SizingProfilePerformance = "performance"
)

// Instance-family preference tiers used by sizing profiles. Cost-conscious
// profiles prefer burstable general-purpose families; the performance profile
// prefers non-burstable compute/memory-optimised families.
var (
	// CostConsciousFamilies is the default family preference: cheapest first.
	CostConsciousFamilies = []string{"t3a", "t3"}
	// PerformanceFamilies prefers non-burstable, performance-oriented families.
	PerformanceFamilies = []string{"m5", "c5", "r5"}
)

// Chart footprint source labels (ChartFootprint.Source).
const (
	ChartFootprintSourceAnnotation = "chart-annotation"
	ChartFootprintSourceCatalog    = "catalog"
	ChartFootprintSourceLLM        = "llm"
)

// Sizing policy targets: which cluster a profile sub-spec applies to.
const (
	SizingTargetUpstream   = "upstream"
	SizingTargetDownstream = "downstream"
)

// Infrastructure (qa-infra-automation) node role names. These differ from the
// rancher-tests role names (in particular controlplane -> cp) and are used when
// emitting the upstream terraform.tfvars node topology.
const (
	InfraRoleEtcd   = "etcd"
	InfraRoleCP     = "cp"
	InfraRoleWorker = "worker"
)

// TriageResults is the output of the "analyze" step.
type TriageResults struct {
	QaseRunID      *int          `json:"qase_run_id,omitempty"`
	TotalTests     int           `json:"total_tests"`
	Passed         []TriageEntry `json:"passed"`
	ProductDefects []TriageEntry `json:"product_defects"`
	TestDefects    []TriageEntry `json:"test_defects"`
	ConfigIssues   []TriageEntry `json:"config_issues"`
}

type TriageClassification string

const (
	// Classification values for triage results.
	ClassPassed            TriageClassification = "passed"
	ClassProductDefect     TriageClassification = "product_defect"
	ClassTestDefect        TriageClassification = "test_defect"
	ClassConfigEnvironment TriageClassification = "config_environment"
	ClassUnknown           TriageClassification = "unknown"
)

type TriageConfidence string

const (
	// Confidence values for triage results.
	ConfidenceHigh   TriageConfidence = "high"
	ConfidenceMedium TriageConfidence = "medium"
	ConfidenceLow    TriageConfidence = "low"
)

type TriageSeverity string

const (
	// Severity values for triage results.
	SeverityTrivial  TriageSeverity = "trivial"
	SeverityMinor    TriageSeverity = "minor"
	SeverityNormal   TriageSeverity = "normal"
	SeverityMajor    TriageSeverity = "major"
	SeverityCritical TriageSeverity = "critical"
	SeverityBlocker  TriageSeverity = "blocker"
)

// TriageEntry describes a single test result after triage classification.
type TriageEntry struct {
	TestName            string               `json:"test_name"`
	Package             string               `json:"package"`
	Error               string               `json:"error,omitempty"`
	DurationMS          int                  `json:"duration_ms,omitempty"`
	Classification      TriageClassification `json:"classification,omitempty"`
	Confidence          TriageConfidence     `json:"confidence,omitempty"`
	Evidence            string               `json:"evidence,omitempty"`
	StackTrace          string               `json:"stack_trace,omitempty"`
	PatternMatched      string               `json:"pattern_matched,omitempty"`
	RecommendedRepo     string               `json:"recommended_repo,omitempty"`
	RecommendedSeverity TriageSeverity       `json:"recommended_severity,omitempty"`
	RecommendedAction   string               `json:"recommended_action,omitempty"`
}

// DefectActions is the output of the "defects" step.
type DefectActions struct {
	IssuesCreated      []CreatedIssue      `json:"issues_created"`
	PRsOpened          []CreatedPR         `json:"prs_opened"`
	CopilotAssignments []CopilotAssignment `json:"copilot_assignments"`
	Escalated          []EscalatedDefect   `json:"escalated"`
}

// CreatedIssue describes a GitHub issue that was created for a defect.
type CreatedIssue struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	DefectType string `json:"defect_type"`
}

// CreatedPR describes a GitHub pull request that was opened.
type CreatedPR struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	ReviewStatus string `json:"review_status,omitempty"`
}

// CopilotAssignment describes a Copilot coding agent assignment to an issue.
type CopilotAssignment struct {
	IssueURL string `json:"issue_url"`
	Repo     string `json:"repo"`
}

// EscalatedDefect describes a defect that requires human attention.
type EscalatedDefect struct {
	TestName string `json:"test_name"`
	Reason   string `json:"reason"`
}

// ConfigActions is the output of the "config-failures" step.
type ConfigActions struct {
	Reruns    []RerunEntry `json:"reruns"`
	Guards    []GuardEntry `json:"guards"`
	PRsOpened []CreatedPR  `json:"prs_opened"`
}

// RerunEntry describes a test that was rerun due to a configuration issue.
type RerunEntry struct {
	TestName      string `json:"test_name"`
	JobName       string `json:"job_name"`
	OriginalError string `json:"original_error"`
	RerunQueueID  *int   `json:"rerun_queue_id"`
	Attempt       int    `json:"attempt"`
}

// GuardEntry describes a guard added to prevent a configuration failure.
type GuardEntry struct {
	TestName     string `json:"test_name"`
	GuardType    string `json:"guard_type"`
	FileModified string `json:"file_modified"`
	Description  string `json:"description"`
}

// CleanupResult is the output of the "cleanup" step.
type CleanupResult struct {
	QaseRunsDeleted    int      `json:"qase_runs_deleted"`
	QaseDefectsDeleted int      `json:"qase_defects_deleted"`
	GithubIssuesClosed int      `json:"github_issues_closed"`
	GithubPRsClosed    int      `json:"github_prs_closed"`
	Errors             []string `json:"errors"`
}

// ---------------------------------------------------------------------------
// Feature Test Mapping types (output of generate-feature-map)
// ---------------------------------------------------------------------------

// FeatureTestMapping is the top-level structure of feature_test_mapping.json.
type FeatureTestMapping struct {
	Metadata     MappingMetadata        `json:"_metadata"`
	FeatureAreas map[string]FeatureArea `json:"feature_areas"`
}

// MappingMetadata holds generation metadata for a mapping file.
type MappingMetadata struct {
	GeneratedAt string `json:"generated_at"`
	GeneratedBy string `json:"generated_by"`
	Version     string `json:"version"`
}

// FeatureArea describes one feature area and its associated test files.
type FeatureArea struct {
	Description     string     `json:"description"`
	ProductPackages []string   `json:"product_packages"`
	TestFiles       []TestFile `json:"test_files"`
	ActionsPackages []string   `json:"actions_packages"`
	JenkinsJobs     []string   `json:"jenkins_jobs"`
	PITTags         []string   `json:"pit_tags"`
}

// TestFile describes a single test file within a feature area.
type TestFile struct {
	Path          string     `json:"path"`
	BuildTags     []string   `json:"build_tags"`
	TestSuite     string     `json:"test_suite"`
	TestFunctions []string   `json:"test_functions"`
	QaseSchema    *string    `json:"qase_schema"`
	QaseProjects  []string   `json:"qase_projects"`
	QaseCases     []QaseCase `json:"qase_cases"`
}

// QaseCase represents a Qase test case entry in the feature test mapping.
// The ID is populated by querying the Qase API during mapping generation.
type QaseCase struct {
	ID                 int    `json:"id"`
	Title              string `json:"title"`
	AutomationTestName string `json:"automation_test_name"`
}

// ---------------------------------------------------------------------------
// Jenkins Trigger Mapping types (output of generate-trigger-map)
// ---------------------------------------------------------------------------

// JenkinsTriggerMapping is the top-level structure of jenkins_trigger_mapping.json.
type JenkinsTriggerMapping struct {
	Metadata             MappingMetadata            `json:"_metadata"`
	JobMappings          map[string]JobMapping      `json:"job_mappings"`
	TagToJob             map[string]string          `json:"tag_to_job"`
	TagToJobHierarchy    map[string][]string        `json:"tag_to_job_hierarchy"`
	QaseProjects         map[string]QaseProjectInfo `json:"qase_projects"`
	QaseParameterMapping map[string]string          `json:"qase_parameter_mapping"`
	JenkinsfileMapping   map[string]string          `json:"jenkinsfile_mapping"`
}

// JobMapping describes a single Jenkins job and its configuration.
type JobMapping struct {
	Description    string                  `json:"description"`
	YAMLSource     string                  `json:"yaml_source"`
	Jenkinsfile    string                  `json:"jenkinsfile"`
	Folder         string                  `json:"folder"`
	QaseProject    string                  `json:"qase_project"`
	QaseReporter   string                  `json:"qase_reporter"`
	ApplicableTags []string                `json:"applicable_tags"`
	Parameters     map[string]JobParameter `json:"parameters"`
}

// JobParameter describes a single parameter of a Jenkins job.
type JobParameter struct {
	Type            string `json:"type"`
	Default         string `json:"default"`
	RequiredForQase bool   `json:"required_for_qase,omitempty"`
}

// QaseProjectInfo describes a Qase project referenced in the trigger mapping.
type QaseProjectInfo struct {
	Name           string   `json:"name"`
	AutomationTags []string `json:"automation_tags"`
}

// ---------------------------------------------------------------------------
// Environment Plan types (output of plan-environment)
// ---------------------------------------------------------------------------

// EnvironmentPlan is the top-level output of the "plan-environment" step. It
// describes the minimum viable test environment(s) required to run the
// identified tests, derived from static analysis of the test sources with an
// LLM fallback for inconclusive cases.
type EnvironmentPlan struct {
	Metadata MappingMetadata `json:"_metadata"`
	PRNumber int             `json:"pr_number"`
	// Strategy is either "single" (one environment satisfies all tests) or
	// "per_group" (each Jenkins-job/build-tag group gets its own environment
	// because a single merged environment was determined to be unsafe).
	Strategy string `json:"strategy"`
	// Groups holds one environment per group. When Strategy == "single" there
	// is exactly one group named "all".
	Groups []EnvironmentGroup `json:"groups"`
	// Upstream is the recommended upstream (Rancher management) cluster
	// topology. It is singular for the whole plan and is populated unless
	// upstream recommendation is disabled (--no-upstream).
	Upstream *UpstreamCluster `json:"upstream,omitempty"`
	// VersionResolution records how each concrete version value was derived
	// when --resolve-versions was passed to plan-environment. Nil when
	// version resolution was not requested (versions remain ${VAR}
	// placeholders in that case).
	VersionResolution *VersionResolution `json:"version_resolution,omitempty"`
}

// VersionResolution is the record for --resolve-versions: it
// captures the resolved value and source tier for every version-bearing
// field, for auditability and debugging.
type VersionResolution struct {
	RancherVersion     ResolvedVersion `json:"rancher_version"`
	RancherImageTag    ResolvedVersion `json:"rancher_image_tag"`
	CertManagerVersion ResolvedVersion `json:"cert_manager_version"`
	// RancherMinor is the resolved Rancher "major.minor" (e.g. "2.15") that
	// drives KDM/chart branch selection and the in-development image tag.
	RancherMinor string `json:"rancher_minor,omitempty"`
	// KubernetesVersionByDistro maps a distro (rke2/k3s) to its resolved
	// Kubernetes version, covering both the upstream management cluster and
	// the downstream cattle-config default.
	KubernetesVersionByDistro map[string]ResolvedVersion `json:"kubernetes_version_by_distro,omitempty"`
	// RancherChartRepoName/RancherChartRepoURL override the Helm repository
	// used to install Rancher when the resolved version isn't published to
	// qa-infra-automation's default "rancher-latest" channel (e.g. alpha
	// prereleases, which are only published to the "rancher-alpha" channel).
	// Both are empty when the default channel is correct for the resolved
	// version.
	RancherChartRepoName string `json:"rancher_chart_repo_name,omitempty"`
	RancherChartRepoURL  string `json:"rancher_chart_repo_url,omitempty"`
}

// ResolvedVersion carries a single resolved version value and the tier that
// produced it.
type ResolvedVersion struct {
	Value string `json:"value"`
	// Source is one of: "override", "tag-compare", "kdm", "release-notes",
	// "images-txt", "upstream-latest", "in-development", "placeholder".
	Source string `json:"source"`
	// Detail carries extra human-readable context (e.g. the release branch or
	// tag examined), useful for debugging.
	Detail string `json:"detail,omitempty"`
}

// UpstreamCluster describes the recommended topology and settings for the
// upstream Rancher management cluster. Unlike downstream clusters, it is not
// derived from the tests; it starts from a fixed baseline and is then shaped by
// the sizing policy. It is rendered into qa-infra-automation input files
// (terraform.tfvars + ansible vars.yaml).
type UpstreamCluster struct {
	// NodePools is the management-cluster machine pools (roles + quantities).
	NodePools []NodeRequirement `json:"node_pools"`
	// KubernetesDistro is the management-cluster distro ("rke2" or "k3s").
	KubernetesDistro string `json:"kubernetes_distro,omitempty"`
	// KubernetesVersion is the management-cluster k8s version (may be a ${VAR}).
	KubernetesVersion string `json:"kubernetes_version,omitempty"`
	// CNI is the management-cluster CNI plugin.
	CNI string `json:"cni,omitempty"`
	// Provider is the cloud/node provider for the management cluster.
	Provider string `json:"provider,omitempty"`
	// Env is the qa-infra deployment environment (e.g. "default", "airgap").
	Env string `json:"env,omitempty"`
	// TotalNodes is the sum of all node-pool quantities.
	TotalNodes int `json:"total_nodes"`
	// AppliedProfile records the sizing profile applied to this cluster.
	AppliedProfile string `json:"applied_profile,omitempty"`
	// ArtifactPaths lists the generated qa-infra files (relative to the upstream
	// output subdirectory). Empty when upstream emission was disabled.
	ArtifactPaths []string `json:"artifact_paths,omitempty"`
	// Warnings captures non-fatal issues from policy application.
	Warnings []string `json:"warnings,omitempty"`
}

// EnvironmentGroup describes the minimum viable environment for one group of
// tests that share a Jenkins job / build-tag set.
type EnvironmentGroup struct {
	// Name identifies the group: "all" for the single strategy, otherwise the
	// Jenkins job name (or build tag) the group is keyed on.
	Name string `json:"name"`
	// JenkinsJobs are the Jenkins jobs this environment serves.
	JenkinsJobs []string `json:"jenkins_jobs,omitempty"`
	// BuildTags are the build tags covered by this environment.
	BuildTags []string `json:"build_tags,omitempty"`
	// TestFiles are the identified test file paths covered by this environment.
	TestFiles []string `json:"test_files,omitempty"`
	// Cluster is the computed minimum viable cluster requirement.
	Cluster ClusterRequirement `json:"cluster"`
	// Workloads are deployments/resources that must be present for the tests.
	Workloads []WorkloadRequirement `json:"workloads,omitempty"`
	// Charts are Helm charts the tests install. These dominate cluster resource
	// sizing (their footprints are resolved from the Rancher charts repo /
	// curated catalog) and are recorded here for auditability.
	Charts []ChartRequirement `json:"charts,omitempty"`
	// CattleConfigPath is the path to the generated cattle-config.yaml for this
	// group, relative to the plan output directory. Empty if generation was
	// disabled.
	CattleConfigPath string `json:"cattle_config_path,omitempty"`
	// DerivedBy records, per test file, how requirements were derived:
	// "static" or "llm". Useful for auditing confidence.
	DerivedBy map[string]string `json:"derived_by,omitempty"`
	// Warnings captures non-fatal issues (e.g. conflicting requirements that
	// were resolved by taking the maximum/superset).
	Warnings []string `json:"warnings,omitempty"`
	// AppliedProfile records the sizing profile applied to this group's
	// downstream cluster (e.g. "minimal", "ha").
	AppliedProfile string `json:"applied_profile,omitempty"`
}

// ClusterRequirement describes the downstream cluster topology and config that
// the tests in a group require. Mirrors the rancher-tests provisioningInput /
// clusterConfig schema closely enough to render a cattle-config.yaml.
type ClusterRequirement struct {
	// NodePools is the minimum set of machine pools (roles + quantities).
	NodePools []NodeRequirement `json:"node_pools"`
	// KubernetesDistro is "rke2", "k3s", or "rke1" (the downstream distro).
	KubernetesDistro string `json:"kubernetes_distro,omitempty"`
	// KubernetesVersion is the required k8s version, empty means "use default".
	KubernetesVersion string `json:"kubernetes_version,omitempty"`
	// CNI is the required CNI plugin (e.g. "calico", "cilium"), empty = default.
	CNI string `json:"cni,omitempty"`
	// Provider is the node/cloud provider (e.g. "aws", "azure", "harvester").
	Provider string `json:"provider,omitempty"`
	// NodeProvider is the lower-level node provider (e.g. "ec2").
	NodeProvider string `json:"node_provider,omitempty"`
	// PSACT is the Pod Security Admission Configuration Template requirement.
	PSACT string `json:"psact,omitempty"`
	// Hardened indicates a CIS-hardened cluster is required.
	Hardened bool `json:"hardened,omitempty"`
	// Networking captures special networking requirements.
	Networking *NetworkingRequirement `json:"networking,omitempty"`
	// Downstream indicates whether a downstream cluster is required at all
	// (some tests only need the local/management cluster).
	Downstream bool `json:"downstream"`
	// TotalNodes is the sum of all node-pool quantities, for quick reference.
	TotalNodes int `json:"total_nodes"`
}

// NodeRequirement is one machine pool: a role set and a node count.
type NodeRequirement struct {
	Etcd         bool   `json:"etcd"`
	ControlPlane bool   `json:"controlplane"`
	Worker       bool   `json:"worker"`
	Windows      bool   `json:"windows,omitempty"`
	Quantity     int    `json:"quantity"`
	Description  string `json:"description,omitempty"`
	// Spec holds the recommended machine size for this pool. It is only
	// populated when plan-environment is run with --recommend-specs; otherwise
	// nil and the cattle-config falls back to the template's machine fields
	// (e.g. ${AWS_INSTANCE_TYPE}).
	Spec *MachineSpec `json:"spec,omitempty"`
}

// MachineSpec is a provider-agnostic recommended machine size for a node pool.
// plan-environment computes these abstract values (with --recommend-specs) and
// the cattle-config generator maps them to provider-specific fields:
//   - AWS: smallest instanceType from the catalog meeting VCPUs/MemoryGiB, plus
//     rootSize = DiskGiB (and awsEC2Configs.volumeSize).
//   - Harvester/vSphere: cpuCount = VCPUs, memorySize = MemoryGiB,
//     diskSize = DiskGiB.
type MachineSpec struct {
	VCPUs     int `json:"vcpus"`
	MemoryGiB int `json:"memory_gib"`
	DiskGiB   int `json:"disk_gib"`
	// InstanceType is the resolved provider instance type (e.g. AWS
	// "t3.xlarge"), set during cattle-config generation when the provider uses
	// named instance types. Empty for providers that take raw cpu/mem/disk.
	InstanceType string `json:"instance_type,omitempty"`
	// Rationale explains how the spec was derived (heuristic factors / LLM).
	Rationale string `json:"rationale,omitempty"`
	// Source is "heuristic" or "llm".
	Source string `json:"source,omitempty"`
}

// NetworkingRequirement captures cluster networking requirements that affect
// the environment (e.g. ACE / local cluster auth endpoint, dual-stack).
type NetworkingRequirement struct {
	LocalClusterAuthEndpoint bool   `json:"local_cluster_auth_endpoint,omitempty"`
	StackPreference          string `json:"stack_preference,omitempty"`
	ClusterCIDR              string `json:"cluster_cidr,omitempty"`
	ServiceCIDR              string `json:"service_cidr,omitempty"`
}

// WorkloadRequirement describes a workload/deployment that tests need present.
type WorkloadRequirement struct {
	Kind        string `json:"kind"` // e.g. "deployment", "daemonset", "statefulset", "ingress"
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ChartRequirement describes a Helm chart a test installs (e.g.
// "rancher-monitoring"). Charts are the dominant driver of downstream cluster
// resource sizing; their resource footprint is resolved separately (from the
// Rancher charts repo's Chart.yaml annotations, a curated catalog, or the LLM).
type ChartRequirement struct {
	// Name is the canonical Rancher chart name (matches the rancher/charts
	// directory name and the actions/charts name constant), e.g.
	// "rancher-monitoring", "longhorn".
	Name string `json:"name"`
	// Detected records how the chart was identified (e.g. the source token), for
	// auditing.
	Detected string `json:"detected,omitempty"`
	// Footprint, when resolved, is the chart's recommended resource request
	// total folded into node sizing. Nil when unresolved.
	Footprint *ChartFootprint `json:"footprint,omitempty"`
}

// ChartFootprint is a chart's total resource request, used to size the worker /
// all-roles pools that host chart workloads.
type ChartFootprint struct {
	CPUMillis int `json:"cpu_millis"`
	MemoryMiB int `json:"memory_mib"`
	DiskGiB   int `json:"disk_gib,omitempty"`
	// Source records where the footprint came from: "chart-annotation",
	// "catalog", or "llm".
	Source string `json:"source,omitempty"`
}
