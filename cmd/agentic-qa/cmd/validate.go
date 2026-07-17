package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/jenkins"
	"github.com/rancher/tests/internal/agenticqa/qase"
)

func init() {
	rootCmd.AddCommand(validateCmd)
}

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate credential connectivity",
	Long:  `Checks GitHub, Qase, Jenkins, and LLM provider credentials.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var failures int

		ghToken := os.Getenv(githubTokenEnvVar)
		if ghToken == "" {
			logrus.Errorf("%s is not set", githubTokenEnvVar)
			failures++
		} else if err := validateGitHub(cmd.Context(), ghToken); err != nil {
			logrus.Errorf("GitHub: %v", err)
			failures++
		} else {
			logrus.Info("GitHub: OK")
		}

		qaseToken := os.Getenv(qaseApiTokenEnvVar)
		if qaseToken == "" {
			logrus.Errorf("%s is not set", qaseApiTokenEnvVar)
			failures++
		} else {
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, qase.BaseURL+"/project", nil)
			if err != nil {
				logrus.Errorf("Qase: failed to create request: %v", err)
				failures++
			} else {
				req.Header.Set("Token", qaseToken)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					logrus.Errorf("Qase: request failed: %v", err)
					failures++
				} else {
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						logrus.Info("Qase: OK")
					} else {
						logrus.Errorf("Qase: unexpected status %d", resp.StatusCode)
						failures++
					}
				}
			}
		}

		jenkinsURL := os.Getenv(jenkinsURLEnvVar)
		if jenkinsURL == "" && runConfig != nil {
			jenkinsURL = runConfig.Jenkins.URL
		}
		jenkinsUser := activeJenkinsUser()
		jenkinsToken := os.Getenv(jenkinsTokenEnvVar)
		switch {
		case jenkinsURL == "":
			logrus.Errorf("%s is not set", jenkinsURLEnvVar)
			failures++
		case jenkinsUser == "":
			logrus.Errorf("%s is not set", jenkinsUserEnvVar)
			failures++
		case jenkinsToken == "":
			logrus.Errorf("%s is not set", jenkinsTokenEnvVar)
			failures++
		default:
			name, err := jenkins.NewClient(jenkinsURL, jenkinsUser, jenkinsToken).ValidateAuth(cmd.Context())
			if err != nil {
				logrus.Errorf("Jenkins: %v", err)
				failures++
			} else {
				logrus.Infof("Jenkins: OK (%s)", name)
			}
		}

		switch provider {
		case llmProviderClaudeDirect:
			apiKey := os.Getenv(claudeAPIKeyEnvVar)
			if apiKey == "" {
				logrus.Errorf("%s is not set", claudeAPIKeyEnvVar)
				failures++
			} else {
				req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, "https://api.anthropic.com/v1/models", nil)
				if err != nil {
					logrus.Errorf("Claude Direct: failed to create request: %v", err)
					failures++
				} else {
					req.Header.Set("x-api-key", apiKey)
					req.Header.Set("anthropic-version", "2023-06-01")
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						logrus.Errorf("Claude Direct: request failed: %v", err)
						failures++
					} else {
						resp.Body.Close()
						if resp.StatusCode == http.StatusOK {
							logrus.Info("Claude Direct: OK")
						} else {
							logrus.Errorf("Claude Direct: unexpected status %d", resp.StatusCode)
							failures++
						}
					}
				}
			}
		case llmProviderVertexAI:
			creds := os.Getenv(googleApplicationCredentialsEnvVar)
			if creds == "" {
				logrus.Errorf("%s is not set", googleApplicationCredentialsEnvVar)
				failures++
			} else if _, err := os.Stat(creds); err != nil {
				logrus.Errorf("Vertex AI: credentials file not found: %s", creds)
				failures++
			} else {
				logrus.Info("Vertex AI: credentials file exists")
			}

			if vertexProject == "" {
				logrus.Error("Vertex AI: --vertex-project is not set")
				failures++
			} else {
				logrus.Infof("Vertex AI: project=%s location=%s", vertexProject, vertexLocation)
			}
		}

		if failures > 0 {
			return fmt.Errorf("%d credential check(s) failed", failures)
		}

		logrus.Info("All credential checks passed")
		return nil
	},
}

func validateGitHub(ctx context.Context, token string) error {
	return validateGitHubWithClient(ctx, http.DefaultClient, "https://api.github.com/user", token, time.Second)
}

func validateGitHubWithClient(ctx context.Context, client *http.Client, endpoint, token string, retryBase time.Duration) error {
	const attempts = 3
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("request failed: %w", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			return nil
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("authentication failed with status %d", resp.StatusCode)
		}

		requestID := resp.Header.Get("X-GitHub-Request-Id")
		if resp.StatusCode < 500 || attempt == attempts {
			return fmt.Errorf("service returned status %d (request ID %s)", resp.StatusCode, requestID)
		}

		delay := time.Duration(attempt) * retryBase
		if retryAfter, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && retryAfter > 0 {
			delay = time.Duration(retryAfter) * time.Second
		}
		logrus.Warnf("GitHub: service returned status %d (request ID %s); retrying in %s", resp.StatusCode, requestID, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil
}
