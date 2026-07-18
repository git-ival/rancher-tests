package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/jenkins"
	"github.com/rancher/tests/internal/agenticqa/qase"
	"github.com/rancher/tests/internal/agenticqa/state"
	"github.com/rancher/tests/internal/agenticqa/types"
)

var (
	triggerIdentifiedTests string
	triggerMappingFile     string
	triggerQaseRunName     string
	triggerTestTimeout     string
	triggerPRNumber        int
	triggerRepo            string
	triggerOutputFile      string
	triggerJenkinsURL      string
	triggerSetupEnvs       string
)

const (
	identifiedTestsFlag    = "identified-tests"
	triggerQaseRunNameFlag = "qase-run-name"
	triggerTestTimeoutFlag = "test-timeout"
)

func init() {
	f := triggerCmd.Flags()
	f.StringVar(&triggerIdentifiedTests, identifiedTestsFlag, "", "Path to identified_tests.json (required)")
	f.StringVar(&triggerMappingFile, triggerMappingFlag, "", "Path to jenkins_trigger_mapping.json (required)")
	f.StringVar(&triggerQaseRunName, triggerQaseRunNameFlag, "", "Qase test run name")
	f.StringVar(&triggerTestTimeout, triggerTestTimeoutFlag, "3h", "Test timeout duration")
	f.IntVar(&triggerPRNumber, prNumberFlag, 0, "Pull request number")
	f.StringVar(&triggerRepo, repoFlag, defaultProductRepo, "Repository (owner/repo)")
	f.StringVar(&triggerOutputFile, outputFileFlag, "", "Path to write triggered_jobs.json (required)")
	f.StringVar(&triggerJenkinsURL, jenkinsURLFlag, "", "Jenkins server URL")
	f.StringVar(&triggerSetupEnvs, "setup-environments", "", "Path to setup_environments.json")

	_ = triggerCmd.MarkFlagRequired(identifiedTestsFlag)
	_ = triggerCmd.MarkFlagRequired(triggerMappingFlag)
	_ = triggerCmd.MarkFlagRequired(outputFileFlag)

	rootCmd.AddCommand(triggerCmd)
}

// createQaseRun creates a REST run using only mapped case IDs.
func createQaseRun(ctx context.Context, project, name, description string, caseIDs []int) (int, error) {
	qaseToken := os.Getenv(qaseApiTokenEnvVar)
	if qaseToken == "" {
		return 0, fmt.Errorf("%s is required for deterministic trigger mode", qaseApiTokenEnvVar)
	}
	if len(caseIDs) == 0 {
		return 0, fmt.Errorf("refusing to create Qase run without explicit case IDs")
	}
	return qase.NewClient(qaseToken).CreateTestRunWithCases(ctx, project, name, description, caseIDs)
}

func collectProjectCaseIDs(identified types.IdentifiedTests, project string) []int {
	indices := identified.TestsByProject[project]
	if len(indices) == 0 {
		return nil
	}

	seen := map[int]struct{}{}
	caseIDs := make([]int, 0)

	for _, idx := range indices {
		if idx < 0 || idx >= len(identified.Tests) {
			continue
		}
		t := identified.Tests[idx]

		// Use project-specific IDs to avoid cross-project cases.
		if t.QaseCasesByProject != nil {
			for _, caseID := range t.QaseCasesByProject[project] {
				if caseID <= 0 {
					continue
				}
				if _, ok := seen[caseID]; ok {
					continue
				}
				seen[caseID] = struct{}{}
				caseIDs = append(caseIDs, caseID)
			}
		} else {
			// Support legacy files with flat case IDs.
			for _, caseID := range t.QaseCaseIDs {
				if caseID <= 0 {
					continue
				}
				if _, ok := seen[caseID]; ok {
					continue
				}
				seen[caseID] = struct{}{}
				caseIDs = append(caseIDs, caseID)
			}
		}
	}

	sort.Ints(caseIDs)
	return caseIDs
}

// lookupJobQaseProject returns the Qase project code declared for a Jenkins job
// in the trigger mapping. Falls back to the global qaseProject flag value.
func lookupJobQaseProject(triggerMapping map[string]any, jobName string) string {
	jobConfig, ok := lookupJobConfig(triggerMapping, jobName)
	if !ok {
		return qaseProject
	}
	if p, ok := jobConfig["qase_project"].(string); ok && p != "" {
		return p
	}
	return qaseProject
}

func resolveJobQaseProject(job string, identified types.IdentifiedTests, triggerMapping map[string]any) (string, error) {
	selected := selectedTestIndices(job, identified, triggerMapping)
	projects := map[string]struct{}{}
	for project, indices := range identified.TestsByProject {
		for _, selectedIndex := range selected {
			if slices.Contains(indices, selectedIndex) {
				projects[project] = struct{}{}
				break
			}
		}
	}
	if len(projects) == 0 {
		for _, index := range selected {
			for _, project := range identified.Tests[index].QaseProjects {
				projects[project] = struct{}{}
			}
		}
	}
	if len(projects) == 1 {
		for project := range projects {
			return project, nil
		}
	}

	configured := lookupJobQaseProject(triggerMapping, job)
	if _, ok := projects[configured]; ok {
		return configured, nil
	}
	if len(projects) == 0 && configured != "" {
		return configured, nil
	}
	return "", fmt.Errorf("job %q matches tests from multiple Qase projects %v; set an explicit valid job project", job, sortedKeysString(projects))
}

// lookupJobConfig returns the mapping entry for a job from either the
// top-level map (legacy) or the nested job_mappings section (current format).
func lookupJobConfig(triggerMapping map[string]any, jobName string) (map[string]any, bool) {
	if jobConfig, ok := triggerMapping[jobName].(map[string]any); ok {
		return jobConfig, true
	}
	jobMappings, _ := triggerMapping["job_mappings"].(map[string]any)
	if jobMappings == nil {
		return nil, false
	}
	jobConfig, ok := jobMappings[jobName].(map[string]any)
	if !ok {
		return nil, false
	}
	return jobConfig, true
}

// resolveTriggerJobs maps recommended jobs or tags to configured Jenkins jobs.
func resolveTriggerJobs(identified types.IdentifiedTests, triggerMapping map[string]any) []string {
	jobs := make([]string, 0, len(identified.RecommendedJobs))
	seen := map[string]struct{}{}

	addIfMapped := func(job string, source string) {
		if job == "" {
			return
		}
		if _, exists := seen[job]; exists {
			return
		}
		if _, ok := lookupJobConfig(triggerMapping, job); !ok {
			logrus.Warnf("Skipping unmapped recommended job %q (source=%s)", job, source)
			return
		}
		seen[job] = struct{}{}
		jobs = append(jobs, job)
	}

	for _, job := range identified.RecommendedJobs {
		addIfMapped(job, "recommended_jobs")
	}

	if len(jobs) == 0 {
		tagToJob, _ := triggerMapping["tag_to_job"].(map[string]any)
		for _, tag := range identified.RecommendedTags {
			if tagToJob == nil {
				break
			}
			if mapped, ok := tagToJob[tag].(string); ok {
				addIfMapped(mapped, "recommended_tags")
			}
		}
	}

	return jobs
}

type triggerJobRuntime struct {
	Folder      string
	Name        string
	Project     string
	Environment string
	Parameters  map[string]string
}

func buildTriggerRuntime(job string, identified types.IdentifiedTests, mapping map[string]any, setup *types.SetupEnvironments, timeout string) (triggerJobRuntime, error) {
	config, ok := lookupJobConfig(mapping, job)
	if !ok {
		return triggerJobRuntime{}, fmt.Errorf("job %q is missing from trigger mapping", job)
	}
	declared, _ := config["parameters"].(map[string]any)
	bindings := resolveJobBindings(config, declared)
	if bindings.QaseRunID == "" {
		return triggerJobRuntime{}, fmt.Errorf("job %q has no Qase run binding", job)
	}
	params := map[string]string{}
	set := func(parameter, value string) error {
		if parameter == "" || value == "" {
			return nil
		}
		if _, ok := declared[parameter]; !ok {
			return fmt.Errorf("job %q binding %q is not a declared parameter", job, parameter)
		}
		params[parameter] = value
		return nil
	}
	if err := set(bindings.Timeout, timeout); err != nil {
		return triggerJobRuntime{}, err
	}

	files, functions, tags := selectorsForJob(job, identified, mapping)
	if bindings.TestPackage != "" && len(files) > 0 {
		if err := set(bindings.TestPackage, testPackageSelector(bindings.TestPackage, files)); err != nil {
			return triggerJobRuntime{}, err
		}
	}
	if len(functions) > 0 {
		if err := set(bindings.TestCase, shellSafeRunSelector(functions)); err != nil {
			return triggerJobRuntime{}, err
		}
	}
	if err := set(bindings.BuildTags, strings.Join(tags, ",")); err != nil {
		return triggerJobRuntime{}, err
	}

	project, err := resolveJobQaseProject(job, identified, mapping)
	if err != nil {
		return triggerJobRuntime{}, err
	}
	runtime := triggerJobRuntime{Project: project, Parameters: params}
	runtime.Folder, _ = config["folder"].(string)
	runtime.Name, _ = config["job_name"].(string)
	if runtime.Name == "" {
		runtime.Name = job
	}
	if setup != nil {
		group, err := findSetupEnvironment(*setup, job)
		if err != nil {
			return triggerJobRuntime{}, err
		}
		runtime.Environment = group.Name
		if len(group.Artifacts) == 0 {
			return triggerJobRuntime{}, fmt.Errorf("environment group %q has no artifacts", group.Name)
		}
		artifact := group.Artifacts[0]
		if bindings.CattleConfig != "" {
			content, err := os.ReadFile(group.CattleConfigPath)
			if err != nil {
				return triggerJobRuntime{}, fmt.Errorf("reading cattle config for %q: %w", group.Name, err)
			}
			if err := set(bindings.CattleConfig, string(content)); err != nil {
				return triggerJobRuntime{}, err
			}
		} else if bindings.EnvironmentURL != "" {
			if err := set(bindings.EnvironmentURL, artifact.URL); err != nil {
				return triggerJobRuntime{}, err
			}
			if err := set(bindings.EnvironmentSHA256, artifact.SHA256); err != nil {
				return triggerJobRuntime{}, err
			}
		} else {
			return triggerJobRuntime{}, fmt.Errorf("job %q has no cattle config or environment URL binding", job)
		}
	}
	return runtime, nil
}

func buildEnvironmentJobRuntime(job string, identified types.IdentifiedTests, mapping map[string]any, setup *types.SetupEnvironments, timeout string) (triggerJobRuntime, error) {
	config, ok := lookupJobConfig(mapping, job)
	if !ok {
		return triggerJobRuntime{}, fmt.Errorf("environment job %q is missing from trigger mapping", job)
	}
	declared, _ := config["parameters"].(map[string]any)
	required := []string{"TERRAFORM_CONFIG", "ANSIBLE_VARIABLES", "CATTLE_TEST_CONFIG", "TEST_PACKAGE", "GOTEST_TESTCASE_VALIDATION", "VALIDATION_TEST_TAGS", "TEST_TIMEOUT", "INDIVIDUAL_JOB", "HOSTNAME_PREFIX"}
	for _, parameter := range required {
		if _, ok := declared[parameter]; !ok {
			return triggerJobRuntime{}, fmt.Errorf("environment job %q does not declare %s", job, parameter)
		}
	}
	params := map[string]string{}
	for name := range declared {
		if value := jobParameterDefault(declared, name); value != "" {
			params[name] = value
		}
	}
	_, functions, tags := selectorsForJob(job, identified, mapping)
	if len(functions) > 0 {
		params["GOTEST_TESTCASE_VALIDATION"] = shellSafeRunSelector(functions)
	}
	params["TEST_PACKAGE"] = "./validation/..."
	params["VALIDATION_TEST_TAGS"] = strings.Join(tags, ",")
	params["PROVISIONING_TAGS"] = ""
	params["TEST_TIMEOUT"] = timeout
	params["HOSTNAME_PREFIX"] = fmt.Sprintf("agentic-%d-%d", identified.PRNumber, time.Now().UTC().Unix())
	if runConfig != nil {
		if runConfig.Tests.RepoURL != "" {
			params["TESTS_REPO_URL"] = runConfig.Tests.RepoURL
		}
		if runConfig.Tests.Branch != "" {
			params["TESTS_BRANCH"] = runConfig.Tests.Branch
		}
		if runConfig.QAInfra.RepoURL != "" {
			params["QA_INFRA_REPO_URL"] = runConfig.QAInfra.RepoURL
		}
		if runConfig.QAInfra.Branch != "" {
			if _, ok := declared["QA_INFRA_BRANCH"]; ok {
				params["QA_INFRA_BRANCH"] = runConfig.QAInfra.Branch
			} else if _, ok := declared["QA_INFRA_REPO_BRANCH"]; ok {
				params["QA_INFRA_REPO_BRANCH"] = runConfig.QAInfra.Branch
			}
		}
		if runConfig.Jenkins.LibraryBranch != "" {
			if _, ok := declared["QA_JENKINS_LIBRARY_BRANCH"]; ok {
				params["QA_JENKINS_LIBRARY_BRANCH"] = runConfig.Jenkins.LibraryBranch
			}
		}
	}
	var plan types.EnvironmentPlan
	if runConfig != nil && loadJSON(runConfig.Paths.Outputs.EnvironmentPlan, &plan) == nil && plan.VersionResolution != nil {
		versions := plan.VersionResolution
		if versions.RancherVersion.Value != "" {
			params["RANCHER_VERSION"] = versions.RancherVersion.Value
		}
		if versions.RancherImageTag.Value != "" {
			params["RANCHER_IMAGE_TAG"] = versions.RancherImageTag.Value
		}
		if versions.RancherChartRepoURL != "" {
			params["RANCHER_CHART_REPO_URL"] = versions.RancherChartRepoURL
		}
		if versions.CertManagerVersion.Value != "" {
			// Rewrite the cert_manager_version literal embedded in ANSIBLE_VARIABLES.
			// The Jenkins pipeline uses the raw ANSIBLE_VARIABLES string verbatim, so
			// top-level parameter substitution does not reach this field.
			if av, ok := params["ANSIBLE_VARIABLES"]; ok && av != "" {
				params["ANSIBLE_VARIABLES"] = rewriteYAMLField(av, "cert_manager_version", versions.CertManagerVersion.Value)
			}
		}
		if version, ok := versions.KubernetesVersionByDistro[types.DistroRKE2]; ok && version.Value != "" {
			params["RKE2_VERSION"] = version.Value
		}
	}
	if setup != nil && len(setup.Groups) == 1 {
		params["CATTLE_TEST_CONFIG"] = jobParameterDefault(declared, "CATTLE_TEST_CONFIG")
	}
	project, err := resolveJobQaseProject(job, identified, mapping)
	if err != nil {
		return triggerJobRuntime{}, err
	}
	folder, _ := config["folder"].(string)
	name, _ := config["job_name"].(string)
	if name == "" {
		name = job
	}
	environment := ""
	if setup != nil && len(setup.Groups) == 1 {
		environment = setup.Groups[0].Name
	}
	return triggerJobRuntime{Folder: folder, Name: name, Project: project, Environment: environment, Parameters: params}, nil
}

// rewriteYAMLField replaces the value of a YAML scalar field in a multi-line
// string without parsing the full document. It matches lines of the form:
//
//	key: "old"    or    key: old
//
// and replaces the value with the new quoted form.  Only the first match is
// replaced. Returns the original string unchanged when the key is not found.
func rewriteYAMLField(content, key, newValue string) string {
	// Match: optional leading whitespace, key, colon, optional space, optional quotes, value
	pattern := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(key) + `:\s*)("?)[^"\n]*("?)(\s*)$`)
	quoted := `"` + newValue + `"`
	replaced := false
	result := pattern.ReplaceAllStringFunc(content, func(line string) string {
		if replaced {
			return line
		}
		replaced = true
		m := pattern.FindStringSubmatch(line)
		if m == nil {
			return line
		}
		// Preserve original quoting style: if original had quotes keep them, else use bare value.
		wasQuoted := m[2] == `"`
		trailing := m[4]
		if wasQuoted {
			return m[1] + quoted + trailing
		}
		return m[1] + newValue + trailing
	})
	return result
}

func shellSafeRunSelector(functions []string) string {
	parts := make([]string, len(functions))
	for i, fn := range functions {
		parts[i] = regexp.QuoteMeta(fn)
	}
	return `-run ^\(` + strings.Join(parts, `\|`) + `\)$`
}

func jobParameterDefault(parameters map[string]any, name string) string {
	parameter, _ := parameters[name].(map[string]any)
	value, _ := parameter["default"].(string)
	return value
}

func testPackageSelector(parameter string, directories []string) string {
	if parameter == "GO_TEST_PACKAGE" {
		if len(directories) == 1 {
			return "./" + filepath.ToSlash(directories[0]) + "/..."
		}
		return "./validation/..."
	}
	if len(directories) == 1 {
		dir := strings.TrimPrefix(filepath.ToSlash(directories[0]), "validation/")
		if dir != "validation" && dir != "." && dir != "" {
			return dir + "/..."
		}
	}
	return "..."
}

func resolveJobBindings(config map[string]any, declared map[string]any) types.JobBindings {
	var b types.JobBindings
	if raw, ok := config["bindings"].(map[string]any); ok {
		b.QaseRunID, _ = raw["qase_run_id"].(string)
		b.TestPackage, _ = raw["test_package"].(string)
		b.TestCase, _ = raw["test_case"].(string)
		b.BuildTags, _ = raw["build_tags"].(string)
		b.Timeout, _ = raw["timeout"].(string)
		b.CattleConfig, _ = raw["cattle_config"].(string)
		b.EnvironmentURL, _ = raw["environment_url"].(string)
		b.EnvironmentSHA256, _ = raw["environment_sha256"].(string)
	}
	pick := func(current *string, names ...string) {
		if *current != "" {
			return
		}
		for _, name := range names {
			if _, ok := declared[name]; ok {
				*current = name
				return
			}
		}
	}
	pick(&b.QaseRunID, "QASE_TEST_RUN_ID", "QASE_RUN_ID")
	pick(&b.TestPackage, "TEST_PACKAGE", "GO_TEST_PACKAGE")
	pick(&b.TestCase, "GOTEST_TESTCASE", "GO_TEST_CASE", "TEST_CASE")
	pick(&b.BuildTags, "TAGS", "GO_TAGS", "VALIDATION_TEST_TAGS")
	pick(&b.Timeout, "TIMEOUT", "GO_TIMEOUT", "TEST_TIMEOUT")
	pick(&b.CattleConfig, "CONFIG", "CATTLE_TEST_CONFIG")
	pick(&b.EnvironmentURL, "AGENTIC_ENV_BUNDLE_URL")
	pick(&b.EnvironmentSHA256, "AGENTIC_ENV_BUNDLE_SHA256")
	return b
}

func selectorsForJob(job string, identified types.IdentifiedTests, mapping map[string]any) ([]string, []string, []string) {
	fileSet, fnSet, tagSet := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, index := range selectedTestIndices(job, identified, mapping) {
		addTestSelectors(identified.Tests[index], fileSet, fnSet, tagSet)
	}
	return sortedKeysString(fileSet), sortedKeysString(fnSet), sortedKeysString(tagSet)
}

func selectedTestIndices(job string, identified types.IdentifiedTests, mapping map[string]any) []int {
	tagToJob, _ := mapping["tag_to_job"].(map[string]any)
	var selected []int
	for index, test := range identified.Tests {
		for _, tag := range test.BuildTags {
			if mapped, _ := tagToJob[tag].(string); mapped == job {
				selected = append(selected, index)
				break
			}
		}
	}
	if len(selected) == 0 {
		selected = make([]int, len(identified.Tests))
		for index := range identified.Tests {
			selected[index] = index
		}
	}
	return selected
}

func addTestSelectors(test types.TestEntry, fileSet, fnSet, tagSet map[string]struct{}) {
	fileSet[filepath.Dir(test.File)] = struct{}{}
	for _, fn := range test.Functions {
		fnSet[fn] = struct{}{}
	}
	for _, tag := range test.BuildTags {
		tagSet[tag] = struct{}{}
	}
}

func sortedKeysString(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func trackedQaseRuns(path string, projects []string, reuse bool) map[string]int {
	runs := map[string]int{}
	if path == "" || !reuse {
		return runs
	}
	tracked, err := state.NewTracker(path).Load()
	if err != nil {
		return runs
	}
	allowed := map[string]struct{}{}
	for _, project := range projects {
		allowed[project] = struct{}{}
	}
	for _, run := range tracked.QaseRuns {
		if _, ok := allowed[run.Project]; ok && run.RunID > runs[run.Project] {
			runs[run.Project] = run.RunID
		}
	}
	return runs
}

// lintJobJenkinsfiles reads the Jenkinsfile for each job from the trigger
// mapping and validates it against the Jenkins Declarative Pipeline linter.
// Files that cannot be read are skipped with a warning (the file may not
// exist locally). Lint failures are fatal — they prevent a build that would
// fail immediately on the controller.
func lintJobJenkinsfiles(ctx context.Context, jClient *jenkins.Client, jobs []string, mapping map[string]any) error {
	jenkinsfileMapping, _ := mapping["jenkinsfile_mapping"].(map[string]any)
	validated := map[string]struct{}{}
	for _, job := range jobs {
		rawPath, _ := jenkinsfileMapping[job].(string)
		if rawPath == "" {
			continue
		}
		if _, already := validated[rawPath]; already {
			continue
		}
		validated[rawPath] = struct{}{}

		// Resolve relative path against validation dir if configured.
		absPath := rawPath
		if runConfig != nil && runConfig.Paths.Inputs.ValidationDir != "" && !filepath.IsAbs(rawPath) {
			absPath = filepath.Join(filepath.Dir(runConfig.Paths.Inputs.ValidationDir), rawPath)
		}

		script, err := os.ReadFile(absPath)
		if err != nil {
			logrus.Warnf("Cannot lint %s (cannot read %s): %v", job, absPath, err)
			continue
		}

		logrus.Infof("Linting Jenkinsfile for %s: %s", job, absPath)
		if err := jClient.ValidateJenkinsfile(ctx, string(script)); err != nil {
			return fmt.Errorf("Jenkinsfile lint failed for job %q (%s): %w", job, absPath, err)
		}
		logrus.Infof("Jenkinsfile lint passed: %s", absPath)
	}
	return nil
}

var triggerCmd = &cobra.Command{
	Use:   "trigger",
	Short: "Trigger Jenkins test jobs",
	Long:  `Loads identified tests and triggers corresponding Jenkins jobs. Creates a Qase test run for tracking.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if stateFile != "" {
			if err := state.NewTracker(stateFile).Ensure(); err != nil {
				return fmt.Errorf("initializing pipeline state: %w", err)
			}
		}

		var identified types.IdentifiedTests
		if err := loadJSON(triggerIdentifiedTests, &identified); err != nil {
			return fmt.Errorf("loading identified tests: %w", err)
		}

		var triggerMapping map[string]any
		if err := loadJSON(triggerMappingFile, &triggerMapping); err != nil {
			return fmt.Errorf("loading trigger mapping: %w", err)
		}

		effectivePRNumber := triggerPRNumber
		if effectivePRNumber <= 0 && identified.PRNumber > 0 {
			effectivePRNumber = identified.PRNumber
			logrus.Infof("Using PR number %d from identified tests (no --pr-number provided)", effectivePRNumber)
		}

		jobsToTrigger := resolveTriggerJobs(identified, triggerMapping)
		if len(jobsToTrigger) == 0 {
			return fmt.Errorf("no mapped Jenkins jobs found from identified tests: recommended_jobs=%v recommended_tags=%v", identified.RecommendedJobs, identified.RecommendedTags)
		}
		var setup *types.SetupEnvironments
		if triggerSetupEnvs != "" {
			setup = &types.SetupEnvironments{}
			if err := loadJSON(triggerSetupEnvs, setup); err != nil {
				return fmt.Errorf("loading setup environments: %w", err)
			}
		}
		if setup != nil && runConfig != nil && runConfig.Jenkins.EnvironmentJob != "" {
			jobsToTrigger = []string{runConfig.Jenkins.EnvironmentJob}
		}
		runtimes := map[string]triggerJobRuntime{}
		for _, job := range jobsToTrigger {
			var runtime triggerJobRuntime
			var err error
			if runConfig != nil && job == runConfig.Jenkins.EnvironmentJob {
				runtime, err = buildEnvironmentJobRuntime(job, identified, triggerMapping, setup, triggerTestTimeout)
			} else {
				runtime, err = buildTriggerRuntime(job, identified, triggerMapping, setup, triggerTestTimeout)
			}
			if err != nil {
				return err
			}
			runtimes[job] = runtime
		}

		environmentManaged := setup != nil && runConfig != nil && runConfig.Jenkins.EnvironmentJob != ""
		reuseRuns := !environmentManaged && (runConfig == nil || !isFile(runConfig.Paths.Outputs.CompletedJobs))
		runMap := trackedQaseRuns(stateFile, identified.QaseProjects, reuseRuns)

		if !dryRun && !environmentManaged {
			baseRunName := triggerQaseRunName
			if baseRunName == "" {
				baseRunName = fmt.Sprintf("PR #%d - Agentic QA", effectivePRNumber)
			}
			if localTest {
				baseRunName = localTestPrefix + baseRunName
			}

			projectsToRun := identified.QaseProjects
			if qaseProject != "" {
				projectsToRun = []string{qaseProject}
				logrus.Infof("--qase-project set: restricting Qase runs to project %s", qaseProject)
			}

			for _, project := range projectsToRun {
				if id := runMap[project]; id > 0 {
					logrus.Infof("Reusing tracked Qase run %d in project %s", id, project)
					continue
				}
				runName := fmt.Sprintf("%s [%s]", baseRunName, project)
				desc := fmt.Sprintf("Automated run for %s#%d (project %s)",
					triggerRepo, effectivePRNumber, project)
				caseIDs := collectProjectCaseIDs(identified, project)

				if len(caseIDs) == 0 {
					return fmt.Errorf("no mapped Qase case IDs for project %s; regenerate mapping and rerun identify", project)
				}

				id, err := createQaseRun(ctx, project, runName, desc, caseIDs)
				if err != nil {
					logrus.Warnf("Failed to create Qase run for project %s: %v — skipping", project, err)
					continue
				}
				logrus.Infof("Created Qase run %d in project %s with %d selected case(s)", id, project, len(caseIDs))
				runMap[project] = id

				if stateFile != "" {
					if trackErr := state.NewTracker(stateFile).AddQaseRun(project, id); trackErr != nil {
						logrus.Warnf("Failed to track Qase run in state file: %v", trackErr)
					}
				}
			}
			if len(runMap) == 0 {
				return fmt.Errorf("no Qase runs created; mapping-driven trigger requires explicit project case mappings")
			}
		}

		jenkinsURL := triggerJenkinsURL
		if jenkinsURL == "" {
			jenkinsURL = os.Getenv(jenkinsURLEnvVar)
		}

		jenkinsUser := activeJenkinsUser()
		jenkinsToken := os.Getenv(jenkinsTokenEnvVar)

		var triggeredJobs []types.TriggeredJob
		jobDisplayName := fmt.Sprintf("Agentic QA PR #%d", effectivePRNumber)
		jobDescription := fmt.Sprintf("Agentic QA run for %s#%d; Qase project(s): %s", triggerRepo, effectivePRNumber, strings.Join(identified.QaseProjects, ", "))

		if jenkinsURL != "" && !dryRun && !localTest {
			jClient := jenkins.NewClient(jenkinsURL, jenkinsUser, jenkinsToken)

			if err := lintJobJenkinsfiles(ctx, jClient, jobsToTrigger, triggerMapping); err != nil {
				return err
			}

			for _, job := range jobsToTrigger {
				runtime := runtimes[job]
				params := runtime.Parameters
				jobProject := runtime.Project
				if runID, ok := runMap[jobProject]; ok {
					bindings := resolveJobBindings(mustJobConfig(triggerMapping, job), mustParameters(triggerMapping, job))
					if bindings.QaseRunID == "" {
						return fmt.Errorf("job %q has no Qase run binding", job)
					}
					params[bindings.QaseRunID] = fmt.Sprintf("%d", runID)
				} else if !environmentManaged {
					return fmt.Errorf("job %q project %q has no Qase run", job, jobProject)
				}

				logrus.Infof("Triggering Jenkins job %s/%s", runtime.Folder, runtime.Name)
				queueID, err := jClient.TriggerBuild(ctx, runtime.Folder, runtime.Name, params)
				if err != nil {
					logrus.Errorf("Failed to trigger %s/%s: %v", runtime.Folder, runtime.Name, err)
					triggeredJobs = append(triggeredJobs, types.TriggeredJob{
						JobName: job, Folder: runtime.Folder, JenkinsJobName: runtime.Name, EnvironmentGroup: runtime.Environment,
						DisplayName: jobDisplayName, Description: jobDescription, Parameters: params, Status: jobStatusTriggerFailed,
					})
					continue
				}

				triggeredJobs = append(triggeredJobs, types.TriggeredJob{
					JobName: job, Folder: runtime.Folder, JenkinsJobName: runtime.Name, EnvironmentGroup: runtime.Environment,
					DisplayName: jobDisplayName, Description: jobDescription, QueueID: &queueID, Parameters: params, Status: jobStatusQueued,
				})
			}
		} else if localTest {
			logrus.Info("Local-test mode: skipping Jenkins trigger; recording jobs as local_test")
			for _, job := range jobsToTrigger {
				triggeredJobs = append(triggeredJobs, types.TriggeredJob{
					JobName: job,
					Status:  jobStatusLocalTest,
				})
			}
		} else if dryRun {
			logrus.Info("Dry-run: skipping Jenkins trigger")
			for _, job := range jobsToTrigger {
				triggeredJobs = append(triggeredJobs, types.TriggeredJob{
					JobName: job,
					Status:  jobStatusDryRun,
				})
			}
		}

		var qaseRuns []types.TriggeredQaseRun
		for proj, id := range runMap {
			qaseRuns = append(qaseRuns, types.TriggeredQaseRun{Project: proj, RunID: id})
		}
		sort.Slice(qaseRuns, func(i, j int) bool {
			return qaseRuns[i].Project < qaseRuns[j].Project
		})

		result := types.TriggeredJobs{
			PayloadVersion: triggerPayloadVersion,
			QaseRuns:       qaseRuns,
			Jobs:           triggeredJobs,
			TriggeredAt:    time.Now().UTC().Format(time.RFC3339),
		}
		// Preserve legacy single-run fields.
		if len(qaseRuns) > 0 {
			result.QaseRunID = &qaseRuns[0].RunID
			result.QaseProject = qaseRuns[0].Project
		}

		if err := saveJSON(triggerOutputFile, result); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		queued := 0
		for _, job := range triggeredJobs {
			if job.Status == jobStatusQueued || job.Status == jobStatusDryRun || job.Status == jobStatusLocalTest {
				queued++
			}
		}
		if len(triggeredJobs) > 0 && queued == 0 {
			return fmt.Errorf("all %d Jenkins job triggers failed; details written to %s", len(triggeredJobs), triggerOutputFile)
		}

		logrus.Infof("Triggered %d of %d jobs → %s", queued, len(triggeredJobs), triggerOutputFile)
		return nil
	},
}

func mustJobConfig(mapping map[string]any, job string) map[string]any {
	config, _ := lookupJobConfig(mapping, job)
	return config
}

func mustParameters(mapping map[string]any, job string) map[string]any {
	params, _ := mustJobConfig(mapping, job)["parameters"].(map[string]any)
	return params
}
