package types

// IdentifiedTests is the output of the "identify" step.
type IdentifiedTests struct {
	PRNumber        int         `json:"pr_number"`
	PRTitle         string      `json:"pr_title"`
	PRURL           string      `json:"pr_url"`
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
