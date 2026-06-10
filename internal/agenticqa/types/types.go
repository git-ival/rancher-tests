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
}

// TestEntry describes a single test identified for execution.
type TestEntry struct {
	File           string   `json:"file"`
	Suite          string   `json:"suite,omitempty"`
	Functions      []string `json:"functions,omitempty"`
	BuildTags      []string `json:"build_tags,omitempty"`
	QaseProjects   []string `json:"qase_projects,omitempty"`
	QaseSchema     string   `json:"qase_schema,omitempty"`
	RelevanceScore float64  `json:"relevance_score,omitempty"`
	Reasoning      string   `json:"reasoning,omitempty"`
}

// TriggeredJobs is the output of the "trigger" step.
type TriggeredJobs struct {
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
	QaseRunID            *int           `json:"qase_run_id"`
	Completed            []CompletedJob `json:"completed"`
	Failed               []CompletedJob `json:"failed"`
	TotalDurationMinutes float64        `json:"total_duration_minutes"`
	CompletedAt          string         `json:"completed_at"`
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

// TriageEntry describes a single test result after triage classification.
type TriageEntry struct {
	TestName            string `json:"test_name"`
	Package             string `json:"package"`
	Error               string `json:"error,omitempty"`
	DurationMS          int    `json:"duration_ms,omitempty"`
	Classification      string `json:"classification,omitempty"`
	Confidence          string `json:"confidence,omitempty"`
	Evidence            string `json:"evidence,omitempty"`
	StackTrace          string `json:"stack_trace,omitempty"`
	PatternMatched      string `json:"pattern_matched,omitempty"`
	RecommendedRepo     string `json:"recommended_repo,omitempty"`
	RecommendedSeverity string `json:"recommended_severity,omitempty"`
	RecommendedAction   string `json:"recommended_action,omitempty"`
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
