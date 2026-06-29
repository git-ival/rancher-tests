package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"

	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	genTriggerMapJJBDirFlag = "jjb-dir"
)

var (
	genTriggerMapJJBDir     string
	genTriggerMapOutputFile string
)

func init() {
	f := genTriggerMapCmd.Flags()
	f.StringVar(&genTriggerMapJJBDir, genTriggerMapJJBDirFlag, "", "Path to jenkins-job-builder directory (required)")
	f.StringVar(&genTriggerMapOutputFile, outputFileFlag, "jenkins_trigger_mapping.json", "Output path for jenkins_trigger_mapping.json")

	_ = genTriggerMapCmd.MarkFlagRequired(genTriggerMapJJBDirFlag)
	rootCmd.AddCommand(genTriggerMapCmd)
}

var genTriggerMapCmd = &cobra.Command{
	Use:   "generate-trigger-map",
	Short: "Generate jenkins_trigger_mapping.json from JJB YAML files",
	Long: `Parses Jenkins Job Builder YAML files to extract job definitions, parameters,
folder structure, Jenkinsfile paths, and applicable build tags. Produces a
jenkins_trigger_mapping.json file used by the trigger subcommand to determine
which Jenkins job to invoke for a given set of test tags.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGenerateTriggerMap()
	},
}

func runGenerateTriggerMap() error {
	logrus.Infof("Scanning JJB directory: %s", genTriggerMapJJBDir)

	yamlFiles, err := findJJBYAMLFiles(genTriggerMapJJBDir)
	if err != nil {
		return fmt.Errorf("finding JJB YAML files: %w", err)
	}
	logrus.Infof("Found %d YAML files", len(yamlFiles))

	// Parse all JJB YAML files.
	allJobs := map[string]*types.JobMapping{}
	allDefaults := map[string]*jjbDefaults{}

	for _, path := range yamlFiles {
		relPath, _ := filepath.Rel(filepath.Dir(genTriggerMapJJBDir), path)
		jobs, defaults, err := parseJJBFile(path, relPath)
		if err != nil {
			logrus.Warnf("Skipping %s: %v", path, err)
			continue
		}
		for name, d := range defaults {
			allDefaults[name] = d
		}
		for name, j := range jobs {
			allJobs[name] = j
		}
	}

	// Resolve defaults inheritance: fill in missing fields from defaults.
	for _, job := range allJobs {
		resolveJobDefaults(job, allDefaults)
	}

	// Filter to only QA-relevant jobs (ones that have test-related parameters).
	filteredJobs := filterQAJobs(allJobs)
	logrus.Infof("Extracted %d QA-relevant jobs (from %d total)", len(filteredJobs), len(allJobs))

	// Build tag→job and hierarchy mappings.
	tagToJob := buildTagToJob(filteredJobs)
	tagHierarchy := buildTagHierarchy(tagToJob)

	// Build qase project info.
	qaseProjects := buildQaseProjectInfo(filteredJobs)

	// Build parameter mapping (which params are Qase-related).
	qaseParamMapping := map[string]string{
		"QASE_TEST_RUN_ID":     "Run ID to report results to",
		"QASE_REPORTER_SCRIPT": "Reporter script that uploads results",
	}

	// Build Jenkinsfile mapping.
	jfMapping := buildJenkinsfileMapping(filteredJobs)

	mapping := types.JenkinsTriggerMapping{
		Metadata: types.MappingMetadata{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			GeneratedBy: "agentic-qa " + genTriggerMapCommandName,
			Version:     mappingFileVersion,
		},
		JobMappings:          filteredJobs,
		TagToJob:             tagToJob,
		TagToJobHierarchy:    tagHierarchy,
		QaseProjects:         qaseProjects,
		QaseParameterMapping: qaseParamMapping,
		JenkinsfileMapping:   jfMapping,
	}

	data, err := json.MarshalIndent(mapping, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling output: %w", err)
	}
	if err := os.WriteFile(genTriggerMapOutputFile, data, 0644); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}

	logrus.Infof("Wrote %s (%d jobs, %d tag mappings)", genTriggerMapOutputFile, len(filteredJobs), len(tagToJob))
	return nil
}

// ---------------------------------------------------------------------------
// JJB YAML parsing types
// ---------------------------------------------------------------------------

// jjbDocument represents one top-level list item in a JJB YAML file.
// JJB files are lists of items, each with a single key like "job", "defaults",
// "scm", "view", etc.
type jjbDocument map[string]interface{}

// jjbDefaults captures the defaults block fields we care about.
type jjbDefaults struct {
	Name        string
	Folder      string
	Jenkinsfile string
}

// findJJBYAMLFiles returns all .yml files in the JJB directory that contain
// job definitions (excludes non-QA files).
func findJJBYAMLFiles(dir string) ([]string, error) {
	var files []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml") {
			// Only include qa-* and harvester-* files that contain job definitions.
			name := strings.ToLower(e.Name())
			if strings.HasPrefix(name, "qa-") || strings.HasPrefix(name, "harvester-") ||
				strings.Contains(name, "recurring") || strings.Contains(name, "freeform") ||
				strings.Contains(name, "tfp-") {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// parseJJBFile reads a single JJB YAML file and extracts job and defaults definitions.
func parseJJBFile(path, relPath string) (map[string]*types.JobMapping, map[string]*jjbDefaults, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	var docs []jjbDocument
	if err := yaml.Unmarshal(data, &docs); err != nil {
		return nil, nil, fmt.Errorf("parsing YAML: %w", err)
	}

	jobs := map[string]*types.JobMapping{}
	defaults := map[string]*jjbDefaults{}

	for _, doc := range docs {
		if jobData, ok := doc["job"]; ok {
			name, job := parseJobEntry(jobData, relPath)
			if name != "" {
				jobs[name] = job
			}
		}
		if defData, ok := doc["defaults"]; ok {
			name, def := parseDefaultsEntry(defData)
			if name != "" {
				defaults[name] = def
			}
		}
	}

	return jobs, defaults, nil
}

// parseJobEntry extracts a JobMapping from a JJB job map.
func parseJobEntry(data interface{}, yamlSource string) (string, *types.JobMapping) {
	m, ok := data.(map[interface{}]interface{})
	if !ok {
		return "", nil
	}

	name := getString(m, "name")
	if name == "" {
		return "", nil
	}

	job := &types.JobMapping{
		Description: getString(m, "description"),
		YAMLSource:  yamlSource,
		Parameters:  map[string]types.JobParameter{},
	}

	// Extract folder from defaults name pattern.
	if defName := getString(m, "defaults"); defName != "" {
		// Will be resolved later from defaults.
		job.Folder = "" // placeholder
	}

	// Extract folder directly if present.
	if folder := getString(m, "folder"); folder != "" {
		job.Folder = folder
	}

	// Parse parameters.
	if params, ok := m["parameters"]; ok {
		if paramList, ok := params.([]interface{}); ok {
			for _, p := range paramList {
				pName, param := parseParameter(p)
				if pName != "" {
					job.Parameters[pName] = param
				}
			}
		}
	}

	// Determine applicable tags: first from TAGS parameter, then infer from job name.
	if tagsParam, ok := job.Parameters["TAGS"]; ok && tagsParam.Default != "" {
		job.ApplicableTags = parseTags(tagsParam.Default)
	}
	if len(job.ApplicableTags) == 0 {
		job.ApplicableTags = inferTagsFromJobName(name)
	}

	// Determine Qase project from job name patterns.
	job.QaseProject = inferQaseProject(name, job.ApplicableTags)

	// Check for QASE_REPORTER_SCRIPT parameter.
	if reporter, ok := job.Parameters["QASE_REPORTER_SCRIPT"]; ok {
		job.QaseReporter = reporter.Default
	}

	return name, job
}

// parseDefaultsEntry extracts relevant fields from a defaults block.
func parseDefaultsEntry(data interface{}) (string, *jjbDefaults) {
	m, ok := data.(map[interface{}]interface{})
	if !ok {
		return "", nil
	}

	name := getString(m, "name")
	if name == "" {
		return "", nil
	}

	def := &jjbDefaults{
		Name:   name,
		Folder: getString(m, "folder"),
	}

	// Extract Jenkinsfile from pipeline-scm.
	if pscm, ok := m["pipeline-scm"]; ok {
		if pscmMap, ok := pscm.(map[interface{}]interface{}); ok {
			def.Jenkinsfile = getString(pscmMap, "script-path")
		}
	}

	return name, def
}

// parseParameter extracts a parameter name and JobParameter from a JJB parameter entry.
func parseParameter(p interface{}) (string, types.JobParameter) {
	pMap, ok := p.(map[interface{}]interface{})
	if !ok {
		return "", types.JobParameter{}
	}

	// JJB parameter format: {"string": {"name": ..., "default": ...}}
	for pType, pData := range pMap {
		typeStr := fmt.Sprintf("%v", pType)
		dataMap, ok := pData.(map[interface{}]interface{})
		if !ok {
			continue
		}

		name := getString(dataMap, "name")
		if name == "" {
			continue
		}

		param := types.JobParameter{
			Type:    typeStr,
			Default: fmt.Sprintf("%v", getDefault(dataMap)),
		}

		// Mark Qase-related parameters.
		if strings.Contains(strings.ToUpper(name), "QASE") && strings.Contains(strings.ToUpper(name), "RUN") {
			param.RequiredForQase = true
		}

		return name, param
	}

	return "", types.JobParameter{}
}

// resolveJobDefaults fills in missing fields from the referenced defaults.
func resolveJobDefaults(job *types.JobMapping, allDefaults map[string]*jjbDefaults) {
	// Try to find matching defaults by name pattern.
	for defName, def := range allDefaults {
		// Check if job's name is built from this default.
		if job.Folder == "" && def.Folder != "" {
			// Match by naming convention.
			if strings.Contains(defName, "individual") && strings.Contains(defName, "updated") {
				if job.Folder == "" {
					job.Folder = def.Folder
				}
			}
		}
		if job.Jenkinsfile == "" && def.Jenkinsfile != "" {
			job.Jenkinsfile = def.Jenkinsfile
		}
	}
}

// inferTagsFromJobName determines applicable tags from a job's name when the
// TAGS parameter is empty, using the active PipelineEnv configuration.
func inferTagsFromJobName(name string) []string {
	return activePipelineEnv().InferTagsFromJobName(name)
}

// filterQAJobs returns only jobs that are relevant to the agentic QA pipeline.
func filterQAJobs(allJobs map[string]*types.JobMapping) map[string]types.JobMapping {
	filtered := map[string]types.JobMapping{}
	for name, job := range allJobs {
		// Include jobs that have GOTEST_TESTCASE or TEST_PACKAGE params
		// (indicating they run Go tests), or have applicable_tags set.
		_, hasTestCase := job.Parameters["GOTEST_TESTCASE"]
		_, hasTestPkg := job.Parameters["TEST_PACKAGE"]
		if hasTestCase || hasTestPkg || len(job.ApplicableTags) > 0 {
			filtered[name] = *job
		}
	}
	return filtered
}

// buildTagToJob creates the tag→job mapping from job definitions.
func buildTagToJob(jobs map[string]types.JobMapping) map[string]string {
	tagToJob := map[string]string{}
	for name, job := range jobs {
		for _, tag := range job.ApplicableTags {
			// Prefer individual jobs over multibranch/orchestrator jobs.
			existing, ok := tagToJob[tag]
			if !ok || preferJob(name, existing) {
				tagToJob[tag] = name
			}
		}
	}
	return tagToJob
}

// preferJob returns true if candidate should replace existing in tag→job mapping.
func preferJob(candidate, existing string) bool {
	// Prefer "individual" jobs (they run single test cases).
	candidateIndiv := strings.Contains(candidate, "individual")
	existingIndiv := strings.Contains(existing, "individual")
	if candidateIndiv && !existingIndiv {
		return true
	}
	return false
}

// buildTagHierarchy groups tags by their hierarchy prefix.
// e.g. "pit.daily", "pit.weekly" → "pit" → ["pit.daily", "pit.weekly"]
func buildTagHierarchy(tagToJob map[string]string) map[string][]string {
	hierarchy := map[string][]string{}
	for tag := range tagToJob {
		parts := strings.Split(tag, ".")
		if len(parts) > 1 {
			prefix := parts[0]
			hierarchy[prefix] = appendUniqueTrigger(hierarchy[prefix], tag)
		}
	}
	// Sort each hierarchy group.
	for k := range hierarchy {
		sort.Strings(hierarchy[k])
	}
	return hierarchy
}

// buildQaseProjectInfo builds the Qase project reference data.
func buildQaseProjectInfo(jobs map[string]types.JobMapping) map[string]types.QaseProjectInfo {
	projects := map[string]*types.QaseProjectInfo{}

	for _, job := range jobs {
		if job.QaseProject == "" {
			continue
		}
		p, ok := projects[job.QaseProject]
		if !ok {
			p = &types.QaseProjectInfo{
				Name: qaseProjectName(job.QaseProject),
			}
			projects[job.QaseProject] = p
		}
		for _, tag := range job.ApplicableTags {
			p.AutomationTags = appendUniqueTrigger(p.AutomationTags, tag)
		}
	}

	result := make(map[string]types.QaseProjectInfo, len(projects))
	for k, v := range projects {
		sort.Strings(v.AutomationTags)
		result[k] = *v
	}
	return result
}

// buildJenkinsfileMapping maps Jenkinsfile paths to the jobs that use them.
func buildJenkinsfileMapping(jobs map[string]types.JobMapping) map[string]string {
	mapping := map[string]string{}
	for name, job := range jobs {
		if job.Jenkinsfile != "" {
			mapping[job.Jenkinsfile] = name
		}
	}
	return mapping
}

// inferQaseProject determines the Qase project for a job based on its tags
// and name, using the active PipelineEnv configuration.
func inferQaseProject(jobName string, tags []string) string {
	return activePipelineEnv().InferQaseProject(jobName, tags)
}

// qaseProjectName returns a human-readable name for a project code using the
// active PipelineEnv configuration.
func qaseProjectName(code string) string {
	return activePipelineEnv().QaseProjectName(code)
}

// parseTags splits a comma or space-separated tag string into individual tags.
func parseTags(s string) []string {
	// Tags can be comma-separated or just a single tag.
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var tags []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' '
	}) {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// getString safely extracts a string value from a map[interface{}]interface{}.
func getString(m map[interface{}]interface{}, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// getDefault extracts the default value from a parameter data map.
func getDefault(m map[interface{}]interface{}) interface{} {
	if v, ok := m["default"]; ok {
		if v == nil {
			return ""
		}
		return v
	}
	return ""
}

// appendUniqueTrigger appends s to slice only if not already present.
func appendUniqueTrigger(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
