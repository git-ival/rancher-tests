package cmd

import (
	"fmt"
	"os"
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

func init() {
	f := triggerCmd.Flags()
	f.StringVar(&triggerIdentifiedTests, "identified-tests", "", "Path to identified_tests.json (required)")
	f.StringVar(&triggerMappingFile, "trigger-mapping", "", "Path to jenkins_trigger_mapping.json (required)")
	f.StringVar(&triggerQaseRunName, "qase-run-name", "", "Qase test run name")
	f.StringVar(&triggerTestTimeout, "test-timeout", "3h", "Test timeout duration")
	f.IntVar(&triggerPRNumber, "pr-number", 0, "Pull request number")
	f.StringVar(&triggerRepo, "repo", "rancher/rancher", "Repository (owner/repo)")
	f.StringVar(&triggerOutputFile, "output-file", "", "Path to write triggered_jobs.json (required)")
	f.StringVar(&triggerJenkinsURL, "jenkins-url", "", "Jenkins server URL")

	_ = triggerCmd.MarkFlagRequired("identified-tests")
	_ = triggerCmd.MarkFlagRequired("trigger-mapping")
	_ = triggerCmd.MarkFlagRequired("output-file")

	rootCmd.AddCommand(triggerCmd)
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

		// Create Qase run
		runName := triggerQaseRunName
		if runName == "" {
			runName = fmt.Sprintf("PR #%d - Agentic QA", triggerPRNumber)
		}

		var qaseRunID *int

		if !dryRun {
			mcpClient := qase.NewMCPClient(mcpURL)
			if mcpClient.IsConfigured() {
				logrus.Info("Creating Qase test run via MCP...")
				id, err := mcpClient.CreateTestRun(ctx, qaseProject, runName, fmt.Sprintf("Automated run for PR #%d", triggerPRNumber))
				if err != nil {
					logrus.Warnf("MCP create run failed, falling back to REST: %v", err)
				} else {
					qaseRunID = &id
				}
			}

			if qaseRunID == nil {
				qaseToken := os.Getenv("QASE_API_TOKEN")
				if qaseToken != "" {
					logrus.Info("Creating Qase test run via REST...")
					qaseClient := qase.NewClient(qaseToken)
					id, err := qaseClient.CreateTestRun(ctx, qaseProject, runName, fmt.Sprintf("Automated run for PR #%d", triggerPRNumber))
					if err != nil {
						logrus.Errorf("Failed to create Qase test run: %v", err)
					} else {
						qaseRunID = &id
					}
				}
			}

			// Track in state file
			if qaseRunID != nil && stateFile != "" {
				tracker := state.NewTracker(stateFile)
				if err := tracker.AddQaseRun(qaseProject, *qaseRunID); err != nil {
					logrus.Warnf("Failed to track Qase run in state file: %v", err)
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

		if jenkinsURL != "" && !dryRun {
			jClient := jenkins.NewClient(jenkinsURL, jenkinsUser, jenkinsToken)

			for _, job := range identified.RecommendedJobs {
				params := map[string]string{
					"TIMEOUT": triggerTestTimeout,
				}
				if triggerPRNumber > 0 {
					params["PR_NUMBER"] = fmt.Sprintf("%d", triggerPRNumber)
				}
				if qaseRunID != nil {
					params["QASE_RUN_ID"] = fmt.Sprintf("%d", *qaseRunID)
				}

				// Look up folder/job from trigger mapping
				jobConfig, _ := triggerMapping[job].(map[string]any)
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
		} else if dryRun {
			logrus.Info("Dry-run: skipping Jenkins trigger")
			for _, job := range identified.RecommendedJobs {
				triggeredJobs = append(triggeredJobs, types.TriggeredJob{
					JobName: job,
					Status:  "dry_run",
				})
			}
		}

		result := types.TriggeredJobs{
			QaseRunID:   qaseRunID,
			QaseProject: qaseProject,
			Jobs:        triggeredJobs,
			TriggeredAt: time.Now().UTC().Format(time.RFC3339),
		}

		if err := saveJSON(triggerOutputFile, result); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		logrus.Infof("Triggered %d jobs → %s", len(triggeredJobs), triggerOutputFile)
		return nil
	},
}


