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

	for _, job := range allJobs {
		resolveJobDefaults(job, allDefaults)
	}

	filteredJobs := filterQAJobs(allJobs)
	logrus.Infof("Extracted %d QA-relevant jobs (from %d total)", len(filteredJobs), len(allJobs))

	tagToJob := buildTagToJob(filteredJobs)
	tagHierarchy := buildTagHierarchy(tagToJob)

	qaseProjects := buildQaseProjectInfo(filteredJobs)

	qaseParamMapping := map[string]string{
		"QASE_TEST_RUN_ID":     "Run ID to report results to",
		"QASE_REPORTER_SCRIPT": "Reporter script that uploads results",
	}

	jfMapping := buildJenkinsfileMapping(filteredJobs)

	mapping := types.JenkinsTriggerMapping{
		Metadata: types.MappingMetadata{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			GeneratedBy: "agentic-qa " + genTriggerMapCommandName,
			Version:     triggerMappingFileVersion,
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
	if err := ensureOutputParent(genTriggerMapOutputFile); err != nil {
		return err
	}
	if err := os.WriteFile(genTriggerMapOutputFile, data, 0644); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}

	logrus.Infof("Wrote %s (%d jobs, %d tag mappings)", genTriggerMapOutputFile, len(filteredJobs), len(tagToJob))
	return nil
}

// jjbDocument represents one top-level JJB list item.
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

	if defName := getString(m, "defaults"); defName != "" {
		job.Defaults = defName
	}

	if folder := getString(m, "folder"); folder != "" {
		job.Folder = folder
	}

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

	if tagsParam, ok := job.Parameters["TAGS"]; ok && tagsParam.Default != "" {
		job.ApplicableTags = parseTags(tagsParam.Default)
	}
	if len(job.ApplicableTags) == 0 {
		job.ApplicableTags = inferTagsFromJobName(name)
	}

	job.QaseProject = inferQaseProject(name, job.ApplicableTags)

	if reporter, ok := job.Parameters["QASE_REPORTER_SCRIPT"]; ok {
		job.QaseReporter = reporter.Default
	}
	job.Bindings = inferJobBindings(job.Parameters)
	job.Capabilities.AcceptsInlineCattleConfig = job.Bindings.CattleConfig != ""
	job.Capabilities.AcceptsEnvironmentURL = job.Bindings.EnvironmentURL != ""
	_, hasTerraform := job.Parameters["TERRAFORM_CONFIG"]
	_, hasAnsible := job.Parameters["ANSIBLE_VARIABLES"]
	job.Capabilities.ProvisionsEnvironment = hasTerraform && hasAnsible

	return name, job
}

func inferJobBindings(parameters map[string]types.JobParameter) types.JobBindings {
	pick := func(names ...string) string {
		for _, name := range names {
			if _, ok := parameters[name]; ok {
				return name
			}
		}
		return ""
	}
	return types.JobBindings{
		QaseRunID:         pick("QASE_TEST_RUN_ID", "QASE_RUN_ID"),
		TestPackage:       pick("TEST_PACKAGE", "GO_TEST_PACKAGE"),
		TestCase:          pick("GOTEST_TESTCASE", "GO_TEST_CASE", "TEST_CASE"),
		BuildTags:         pick("TAGS", "GO_TAGS", "VALIDATION_TEST_TAGS"),
		Timeout:           pick("TIMEOUT", "GO_TIMEOUT", "TEST_TIMEOUT"),
		CattleConfig:      pick("CONFIG", "CATTLE_TEST_CONFIG"),
		EnvironmentURL:    pick("AGENTIC_ENV_BUNDLE_URL"),
		EnvironmentSHA256: pick("AGENTIC_ENV_BUNDLE_SHA256"),
	}
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

		if strings.Contains(strings.ToUpper(name), "QASE") && strings.Contains(strings.ToUpper(name), "RUN") {
			param.RequiredForQase = true
		}

		return name, param
	}

	return "", types.JobParameter{}
}

// resolveJobDefaults fills in missing fields from the referenced defaults.
func resolveJobDefaults(job *types.JobMapping, allDefaults map[string]*jjbDefaults) {
	def := allDefaults[job.Defaults]
	if def == nil {
		return
	}
	if job.Folder == "" {
		job.Folder = def.Folder
	}
	if job.Jenkinsfile == "" {
		job.Jenkinsfile = def.Jenkinsfile
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
		// Keep jobs that run Go tests or declare applicable tags.
		_, hasTestCase := job.Parameters["GOTEST_TESTCASE"]
		_, hasGoTestCase := job.Parameters["GO_TEST_CASE"]
		_, hasTFPTestCase := job.Parameters["TEST_CASE"]
		_, hasTestPkg := job.Parameters["TEST_PACKAGE"]
		_, hasGoTestPkg := job.Parameters["GO_TEST_PACKAGE"]
		if hasTestCase || hasGoTestCase || hasTFPTestCase || hasTestPkg || hasGoTestPkg || len(job.ApplicableTags) > 0 {
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
			existing, ok := tagToJob[tag]
			if !ok || preferJob(name, existing) {
				tagToJob[tag] = name
			}
		}
	}
	return tagToJob
}

// preferJob favors single-test jobs over orchestrators.
func preferJob(candidate, existing string) bool {
	candidateIndiv := strings.Contains(candidate, "individual")
	existingIndiv := strings.Contains(existing, "individual")
	if candidateIndiv && !existingIndiv {
		return true
	}
	return false
}

// buildTagHierarchy groups tags by prefix, such as "pit.daily" under "pit".
func buildTagHierarchy(tagToJob map[string]string) map[string][]string {
	hierarchy := map[string][]string{}
	for tag := range tagToJob {
		parts := strings.Split(tag, ".")
		if len(parts) > 1 {
			prefix := parts[0]
			hierarchy[prefix] = appendUniqueTrigger(hierarchy[prefix], tag)
		}
	}
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
