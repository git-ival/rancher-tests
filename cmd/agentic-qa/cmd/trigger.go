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

		runMap := map[string]int{}

		if !dryRun {
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

		jenkinsUser := os.Getenv(jenkinsUserEnvVar)
		jenkinsToken := os.Getenv(jenkinsTokenEnvVar)

		var triggeredJobs []types.TriggeredJob

		if jenkinsURL != "" && !dryRun && !localTest {
			jClient := jenkins.NewClient(jenkinsURL, jenkinsUser, jenkinsToken)

			for _, job := range jobsToTrigger {
				params := map[string]string{
					jenkinsParamTimeout: triggerTestTimeout,
				}
				if effectivePRNumber > 0 {
					params[jenkinsParamPRNumber] = fmt.Sprintf("%d", effectivePRNumber)
				}
				jobProject := lookupJobQaseProject(triggerMapping, job)
				if runID, ok := runMap[jobProject]; ok {
					params[jenkinsParamQaseRunID] = fmt.Sprintf("%d", runID)
				} else if len(runMap) > 0 {
					// Job's declared project has no run; use the first available run.
					for _, id := range runMap {
						params[jenkinsParamQaseRunID] = fmt.Sprintf("%d", id)
						break
					}
				}

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
						Status:  jobStatusTriggerFailed,
					})
					continue
				}

				triggeredJobs = append(triggeredJobs, types.TriggeredJob{
					JobName:    job,
					QueueID:    &queueID,
					Parameters: params,
					Status:     jobStatusQueued,
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
			QaseRuns:    qaseRuns,
			Jobs:        triggeredJobs,
			TriggeredAt: time.Now().UTC().Format(time.RFC3339),
		}
		// Preserve legacy single-run fields.
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
