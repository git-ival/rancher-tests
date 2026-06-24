// Package envconfig loads and validates the pipeline_env.json configuration
// file that externalises organisation- and environment-specific values from
// the agentic-qa binary.
//
// pipeline_env.json is intentionally excluded from VCS (see .gitignore) and
// is supplied at runtime — typically via a Jenkins text parameter, the same
// pattern used for jenkins_trigger_mapping.json.
//
// Use Generate() to produce a pre-populated template the operator can save
// and customise, then Load() to read it back at runtime.
package envconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// PipelineEnv holds all organisation- and environment-specific configuration
// that was previously compiled into the agentic-qa binary as constants.
type PipelineEnv struct {
	// ---- Qase configuration ------------------------------------------------

	// QaseATNFieldID is the numeric ID of the Qase custom field that stores
	// the AutomationTestName (Go test function name).  In the Rancher workspace
	// this is field 15.
	QaseATNFieldID int `json:"qase_atn_field_id"`

	// QaseProjects is the ordered list of Qase project codes to query when
	// resolving test case IDs or performing title-based fallback matching.
	QaseProjects []string `json:"qase_projects"`

	// QaseProjectNames maps each project code to a human-readable display name
	// used in log messages and generated mapping metadata.
	QaseProjectNames map[string]string `json:"qase_project_names"`

	// ---- Jenkins / build-tag configuration ---------------------------------

	// TagToJob maps a build tag (e.g. "pit.daily") to the canonical Jenkins
	// job name that runs tests carrying that tag.  This is the same data that
	// appears in jenkins_trigger_mapping.json's "tag_to_job" section; keeping
	// both in sync is the operator's responsibility.
	TagToJob map[string]string `json:"tag_to_job"`

	// TagToQaseProject maps a build tag prefix or full tag to the Qase project
	// code that should receive results for jobs triggered by that tag.
	// Keys may be full tags ("pit.daily") or prefixes ("pit.") — the lookup
	// tries the full tag first, then the longest matching prefix.
	TagToQaseProject map[string]string `json:"tag_to_qase_project"`

	// JobNamePatternToQaseProject maps a substring of a Jenkins job name to a
	// Qase project code.  Used by generate-trigger-map when tag-based lookup
	// produces no match.  Keys are matched with strings.Contains (case-insensitive).
	JobNamePatternToQaseProject map[string]string `json:"job_name_pattern_to_qase_project"`

	// JobNameTagPatterns is an ordered list of rules used by generate-trigger-map
	// to infer applicable build tags from a Jenkins job name when the TAGS
	// parameter is not set.  Rules are evaluated in order; the first matching
	// pattern in each rule wins (use exclusive=true to skip remaining patterns
	// in the same rule group).  Pattern matching is case-insensitive.
	JobNameTagPatterns []JobNameTagPattern `json:"job_name_tag_patterns"`

	// DefaultQaseProject is the Qase project code to assign when no tag or
	// job-name pattern produces a match.
	DefaultQaseProject string `json:"default_qase_project"`

	// ---- Repository configuration ------------------------------------------

	// ProductRepo is the "owner/repo" slug for the primary product repository
	// (used when filing product-defect GitHub issues).
	ProductRepo string `json:"product_repo"`

	// TestsRepo is the "owner/repo" slug for the test repository (used when
	// filing test-defect GitHub issues and config-failure PRs).
	TestsRepo string `json:"tests_repo"`

	// ---- GitHub configuration ----------------------------------------------

	// CopilotUsername is the GitHub username of the Copilot / coding-agent bot
	// that is assigned to auto-created defect issues.
	CopilotUsername string `json:"copilot_username"`

	// AgenticQALabel is the GitHub issue label applied to every issue and PR
	// created by the pipeline.
	AgenticQALabel string `json:"agentic_qa_label"`

	// ---- LLM prompt configuration ------------------------------------------

	// ProjectDisplayName is a short human-readable name for the project being
	// tested, injected into LLM system prompts so the model has product context
	// (e.g. "Rancher Kubernetes management platform").
	ProjectDisplayName string `json:"project_display_name"`
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
		QaseATNFieldID: 15,
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
	case "product_defect":
		return e.ProductRepo
	case "test_defect":
		return e.TestsRepo
	default:
		return ""
	}
}

// containsFold is a case-insensitive strings.Contains.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
