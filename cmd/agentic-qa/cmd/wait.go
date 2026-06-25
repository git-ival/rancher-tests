package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/jenkins"
	"github.com/rancher/tests/internal/agenticqa/qase"
	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	waitTriggeredJobsFlag = "triggered-jobs"
	waitPollIntervalFlag  = "poll-interval"

)

var (
	waitTriggeredJobs string
	waitPollInterval  int
	waitOutputFile    string
	waitJenkinsURL    string
)

func init() {
	f := waitCmd.Flags()
	f.StringVar(&waitTriggeredJobs, waitTriggeredJobsFlag, "", "Path to triggered_jobs.json (required)")
	f.IntVar(&waitPollInterval, waitPollIntervalFlag, 120, "Poll interval in seconds")
	f.StringVar(&waitOutputFile, outputFileFlag, "", "Path to write completed_jobs.json (required)")
	f.StringVar(&waitJenkinsURL, jenkinsURLFlag, "", "Jenkins server URL")

	_ = waitCmd.MarkFlagRequired(waitTriggeredJobsFlag)
	_ = waitCmd.MarkFlagRequired(outputFileFlag)

	rootCmd.AddCommand(waitCmd)
}

// completeQaseRun marks a Qase test run as complete, trying MCP first then REST.
// Logs warnings on failure but does not return an error — completion is best-effort.
func completeQaseRun(ctx context.Context, project string, runID int) {
	mcpClient := qase.NewMCPClient(mcpURL)
	if mcpClient.IsConfigured() {
		if _, err := mcpClient.CallTool(ctx, qase.MCPToolCompleteRun, map[string]any{
			qase.MCPArgCode: project,
			qase.MCPArgID:   runID,
		}); err != nil {
			logrus.Warnf("MCP complete run %d (%s) failed: %v", runID, project, err)
		} else {
			logrus.Infof("Completed Qase run %d (%s) via MCP", runID, project)
			return
		}
	}
	qaseToken := os.Getenv(qaseApiTokenEnvVar)
	if qaseToken == "" {
		logrus.Warnf("Cannot complete Qase run %d (%s): no token and MCP not configured", runID, project)
		return
	}
	if err := qase.NewClient(qaseToken).CompleteTestRun(ctx, project, runID); err != nil {
		logrus.Warnf("Failed to complete Qase run %d (%s) via REST: %v", runID, project, err)
	} else {
		logrus.Infof("Completed Qase run %d (%s) via REST", runID, project)
	}
}

var waitCmd = &cobra.Command{
	Use:   "wait",
	Short: "Wait for test completion",
	Long:  `Polls Jenkins for each triggered job until all reach a terminal state, then completes the Qase run.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		startTime := time.Now()

		var triggered types.TriggeredJobs
		if err := loadJSON(waitTriggeredJobs, &triggered); err != nil {
			return fmt.Errorf("loading triggered jobs: %w", err)
		}

		jenkinsURL := waitJenkinsURL
		if jenkinsURL == "" {
		jenkinsURL = os.Getenv(jenkinsURLEnvVar)
		}

		jenkinsUser := os.Getenv(jenkinsUserEnvVar)
		jenkinsToken := os.Getenv(jenkinsTokenEnvVar)
		jClient := jenkins.NewClient(jenkinsURL, jenkinsUser, jenkinsToken)

		// Resolve build numbers from queue IDs first
		for i := range triggered.Jobs {
			job := &triggered.Jobs[i]
			if job.QueueID != nil && job.BuildNumber == nil {
				logrus.Infof("Resolving build number for %s (queue %d)", job.JobName, *job.QueueID)
				buildNum, err := jClient.GetQueueBuildNumber(ctx, *job.QueueID)
				if err != nil {
					logrus.Warnf("Could not resolve build number for %s: %v", job.JobName, err)
				} else {
					job.BuildNumber = &buildNum
				}
			}
		}

		// Poll until all jobs are terminal
		var completed []types.CompletedJob
		var failed []types.CompletedJob
		pending := make(map[int]*types.TriggeredJob)

		for i := range triggered.Jobs {
			job := &triggered.Jobs[i]
			if job.BuildNumber != nil && !isTerminalStatus(job.Status) {
				pending[i] = job
			} else if job.Status == jobStatusTriggerFailed || job.Status == jobStatusDryRun {
				completed = append(completed, types.CompletedJob{
					JobName: job.JobName,
					Status:  job.Status,
				})
			}
		}

		pollDuration := time.Duration(waitPollInterval) * time.Second

		for len(pending) > 0 {
			logrus.Infof("Waiting for %d jobs... (poll interval: %s)", len(pending), pollDuration)
			time.Sleep(pollDuration)

			for idx, job := range pending {
				// Extract folder/job from the job name
				folder, jobName := splitJobName(job.JobName)

				status, err := jClient.GetBuildStatus(ctx, folder, jobName, *job.BuildNumber)
				if err != nil {
					logrus.Warnf("Error polling %s #%d: %v", job.JobName, *job.BuildNumber, err)
					continue
				}

				if status.Result != jobStatusInProgress {
					cj := types.CompletedJob{
						JobName:         job.JobName,
						BuildNumber:     job.BuildNumber,
						Status:          status.Result,
						DurationMinutes: float64(status.DurationMS) / 60000.0,
						LogURL:          status.LogURL,
					}

					if status.Result == jobStatusSuccess {
						completed = append(completed, cj)
					} else {
						failed = append(failed, cj)
					}
					delete(pending, idx)

					logrus.Infof("Job %s #%d finished: %s (%.1f min)",
						job.JobName, *job.BuildNumber, status.Result, cj.DurationMinutes)
				}
			}
		}

		// Complete all Qase runs tracked in this triggered jobs file.
		// Falls back to the legacy single-run fields for backward compat.
		if !dryRun {
			runsToComplete := triggered.QaseRuns
			if len(runsToComplete) == 0 && triggered.QaseRunID != nil {
				runsToComplete = []types.TriggeredQaseRun{
					{Project: triggered.QaseProject, RunID: *triggered.QaseRunID},
				}
			}
			for _, run := range runsToComplete {
				completeQaseRun(ctx, run.Project, run.RunID)
			}
		}

		result := types.CompletedJobs{
			QaseRuns:             triggered.QaseRuns, // propagate for downstream consumers
			QaseRunID:            triggered.QaseRunID, // backward compat
			Completed:            completed,
			Failed:               failed,
			TotalDurationMinutes: time.Since(startTime).Minutes(),
			CompletedAt:          time.Now().UTC().Format(time.RFC3339),
		}

		if err := saveJSON(waitOutputFile, result); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		logrus.Infof("All jobs complete: %d passed, %d failed → %s",
			len(completed), len(failed), waitOutputFile)
		return nil
	},
}

func isTerminalStatus(s string) bool {
	switch s {
	case jobStatusSuccess, jobStatusFailure, jobStatusUnstable, jobStatusAborted, jobStatusTriggerFailed, jobStatusDryRun:
		return true
	}
	return false
}

func splitJobName(name string) (folder, jobName string) {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", name
}
