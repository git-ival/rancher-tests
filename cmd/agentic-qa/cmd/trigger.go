package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
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
)

const (
	identifiedTestsFlag    = "identified-tests"
	triggerMappingFileFlag = "trigger-mapping"
	triggerQaseRunNameFlag = "qase-run-name"
	triggerTestTimeoutFlag = "test-timeout"
	triggerPRNumberFlag    = "pr-number"
	triggerRepoFlag        = "repo"
	triggerOutputFileFlag  = "output-file"
	triggerJenkinsURLFlag  = "jenkins-url"
)

func init() {
	f := triggerCmd.Flags()
	f.StringVar(&triggerIdentifiedTests, identifiedTestsFlag, "", "Path to identified_tests.json (required)")
	f.StringVar(&triggerMappingFile, triggerMappingFileFlag, "", "Path to jenkins_trigger_mapping.json (required)")
	f.StringVar(&triggerQaseRunName, triggerQaseRunNameFlag, "", "Qase test run name")
	f.StringVar(&triggerTestTimeout, triggerTestTimeoutFlag, "3h", "Test timeout duration")
	f.IntVar(&triggerPRNumber, triggerPRNumberFlag, 0, "Pull request number")
	f.StringVar(&triggerRepo, triggerRepoFlag, "rancher/rancher", "Repository (owner/repo)")
	f.StringVar(&triggerOutputFile, triggerOutputFileFlag, "", "Path to write triggered_jobs.json (required)")
	f.StringVar(&triggerJenkinsURL, triggerJenkinsURLFlag, "", "Jenkins server URL")

	_ = triggerCmd.MarkFlagRequired(identifiedTestsFlag)
	_ = triggerCmd.MarkFlagRequired(triggerMappingFileFlag)
	_ = triggerCmd.MarkFlagRequired(triggerOutputFileFlag)

	rootCmd.AddCommand(triggerCmd)
}

// createQaseRun creates a Qase test run, trying MCP first then REST.
// Returns 0 and a non-nil error if both attempts fail.
func createQaseRun(ctx context.Context, project, name, description string, caseIDs []int) (int, error) {
	qaseToken := os.Getenv(qaseApiTokenEnvVar)
	if qaseToken != "" {
		return qase.NewClient(qaseToken).CreateTestRunWithCases(ctx, project, name, description, caseIDs)
	}

	// MCP fallback path when token is not available.
	mcpClient := qase.NewMCPClient(mcpURL)
	if !mcpClient.IsConfigured() {
		return 0, fmt.Errorf("%s not set and MCP not configured", qaseApiTokenEnvVar)
	}
	id, err := mcpClient.CreateTestRun(ctx, project, name, description)
	if err != nil {
		return 0, err
	}
	if len(caseIDs) > 0 {
		logrus.Warnf("Qase MCP run created for %s without explicit case assignment; configure %s to attach %d selected cases", project, qaseApiTokenEnvVar, len(caseIDs))
	} else {
		logrus.Warnf("Qase MCP run created for %s without include_all_cases control; configure %s for deterministic case population", project, qaseApiTokenEnvVar)
	}
	return id, nil
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

		// Prefer QaseCasesByProject (per-project validated IDs) over the
		// flat QaseCaseIDs list. This ensures we only send case IDs that
		// actually belong to this specific project.
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
			// Legacy fallback: use flat QaseCaseIDs (pre-existing identified_tests.json
			// files that don't have the per-project breakdown).
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

// resolveTriggerJobs determines the Jenkins jobs to trigger based on the identified tests and the trigger mapping.
// It first considers the explicitly recommended jobs, then falls back to mapping recommended tags to jobs.
// Only jobs that have a corresponding entry in the trigger mapping are included.
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

var triggerCmd = &cobra.Command{
	Use:   "trigger",
	Short: "Trigger Jenkins test jobs",
	Long:  `Loads identified tests and triggers corresponding Jenkins jobs. Creates a Qase test run for tracking.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

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

		// runMap maps Qase project code → run ID for all runs created this pipeline.
		runMap := map[string]int{}

		if !dryRun {
			baseRunName := triggerQaseRunName
			if baseRunName == "" {
				baseRunName = fmt.Sprintf("PR #%d - Agentic QA", effectivePRNumber)
			}
			if localTest {
				baseRunName = localTestPrefix + baseRunName
			}

			// Determine which projects to create runs for.
			// If --qase-project is set, restrict to that single project;
			// otherwise create a run for every project that has identified cases.
			projectsToRun := identified.QaseProjects
			if qaseProject != "" {
				projectsToRun = []string{qaseProject}
				logrus.Infof("--qase-project set: restricting Qase runs to project %s", qaseProject)
			}

			for _, project := range projectsToRun {
				runName := fmt.Sprintf("%s [%s]", baseRunName, project)
				desc := fmt.Sprintf("Automated run for %s#%d (project %s)",
					triggerRepo, effectivePRNumber, project)
				caseIDs := collectProjectCaseIDs(identified, project)

				if len(caseIDs) == 0 {
					logrus.Warnf("No Qase case IDs for project %s — skipping run creation (tests may belong to other projects)", project)
					continue
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

			// Fallback: if identified.QaseProjects was empty or all creations failed,
			// create a single run in the --qase-project flag value (original behavior).
			if len(runMap) == 0 {
				fallbackProject := qaseProject
				if fallbackProject == "" && len(identified.QaseProjects) > 0 {
					fallbackProject = identified.QaseProjects[0]
					logrus.Infof("--qase-project not set; using first identified project: %s", fallbackProject)
				}
				if fallbackProject == "" {
					logrus.Warn("No Qase project available for fallback run creation — skipping Qase tracking")
				} else {
					logrus.Warn("No per-project Qase runs created; falling back to single run")
					fallbackDesc := fmt.Sprintf("Automated run for %s#%d", triggerRepo, effectivePRNumber)
					id, err := createQaseRun(ctx, fallbackProject, baseRunName, fallbackDesc, nil)
					if err != nil {
						logrus.Errorf("Fallback Qase run creation also failed: %v", err)
					} else {
						logrus.Infof("Created fallback Qase run %d in project %s", id, fallbackProject)
						runMap[fallbackProject] = id
						if stateFile != "" {
							if trackErr := state.NewTracker(stateFile).AddQaseRun(fallbackProject, id); trackErr != nil {
								logrus.Warnf("Failed to track fallback Qase run: %v", trackErr)
							}
						}
					}
				}
			}
		}

		// Trigger Jenkins jobs
		jenkinsURL := triggerJenkinsURL
		if jenkinsURL == "" {
			jenkinsURL = os.Getenv("JENKINS_URL")
		}

		jenkinsUser := os.Getenv("JENKINS_USER")
		jenkinsToken := os.Getenv("JENKINS_TOKEN")

		var triggeredJobs []types.TriggeredJob

		if jenkinsURL != "" && !dryRun && !localTest {
			jClient := jenkins.NewClient(jenkinsURL, jenkinsUser, jenkinsToken)

			for _, job := range jobsToTrigger {
				params := map[string]string{
					"TIMEOUT": triggerTestTimeout,
				}
				if effectivePRNumber > 0 {
					params["PR_NUMBER"] = fmt.Sprintf("%d", effectivePRNumber)
				}
				jobProject := lookupJobQaseProject(triggerMapping, job)
				if runID, ok := runMap[jobProject]; ok {
					params["QASE_RUN_ID"] = fmt.Sprintf("%d", runID)
				} else if len(runMap) > 0 {
					// Job's declared project has no run; use the first available run.
					for _, id := range runMap {
						params["QASE_RUN_ID"] = fmt.Sprintf("%d", id)
						break
					}
				}

				// Look up folder/job from trigger mapping
				jobConfig, _ := lookupJobConfig(triggerMapping, job)
				folder, _ := jobConfig["folder"].(string)
				jobName, _ := jobConfig["job_name"].(string)
				if jobName == "" {
					jobName = job
				}

				logrus.Infof("Triggering Jenkins job %s/%s", folder, jobName)
				queueID, err := jClient.TriggerBuild(ctx, folder, jobName, params)
				if err != nil {
					logrus.Errorf("Failed to trigger %s/%s: %v", folder, jobName, err)
					triggeredJobs = append(triggeredJobs, types.TriggeredJob{
						JobName: job,
						Status:  "trigger_failed",
					})
					continue
				}

				triggeredJobs = append(triggeredJobs, types.TriggeredJob{
					JobName:    job,
					QueueID:    &queueID,
					Parameters: params,
					Status:     "queued",
				})
			}
		} else if localTest {
			logrus.Info("Local-test mode: skipping Jenkins trigger; recording jobs as local_test")
			for _, job := range jobsToTrigger {
				triggeredJobs = append(triggeredJobs, types.TriggeredJob{
					JobName: job,
					Status:  "local_test",
				})
			}
		} else if dryRun {
			logrus.Info("Dry-run: skipping Jenkins trigger")
			for _, job := range jobsToTrigger {
				triggeredJobs = append(triggeredJobs, types.TriggeredJob{
					JobName: job,
					Status:  "dry_run",
				})
			}
		}

		// Build the QaseRuns slice from runMap for multi-project tracking.
		var qaseRuns []types.TriggeredQaseRun
		for proj, id := range runMap {
			qaseRuns = append(qaseRuns, types.TriggeredQaseRun{Project: proj, RunID: id})
		}
		sort.Slice(qaseRuns, func(i, j int) bool {
			return qaseRuns[i].Project < qaseRuns[j].Project
		})

		result := types.TriggeredJobs{
			QaseRuns:    qaseRuns,
			Jobs:        triggeredJobs,
			TriggeredAt: time.Now().UTC().Format(time.RFC3339),
		}
		// Backward compat: populate legacy single-run fields from the first run.
		if len(qaseRuns) > 0 {
			result.QaseRunID = &qaseRuns[0].RunID
			result.QaseProject = qaseRuns[0].Project
		}

		if err := saveJSON(triggerOutputFile, result); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		logrus.Infof("Triggered %d jobs → %s", len(triggeredJobs), triggerOutputFile)
		return nil
	},
}
