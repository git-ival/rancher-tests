package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rancher/tests/internal/agenticqa/qase"
)

func init() {
	rootCmd.AddCommand(validateCmd)
}

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate credential connectivity",
	Long:  `Checks GitHub, Qase, and LLM provider credentials are valid and reachable.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var failures int

		// 1. GitHub token
		ghToken := os.Getenv(githubTokenEnvVar)
		if ghToken == "" {
			logrus.Errorf("%s is not set", githubTokenEnvVar)
			failures++
		} else {
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, "https://api.github.com/user", nil)
			if err != nil {
				logrus.Errorf("GitHub: failed to create request: %v", err)
				failures++
			} else {
				req.Header.Set("Authorization", "Bearer "+ghToken)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					logrus.Errorf("GitHub: request failed: %v", err)
					failures++
				} else {
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						logrus.Info("GitHub: OK")
					} else {
						logrus.Errorf("GitHub: unexpected status %d", resp.StatusCode)
						failures++
					}
				}
			}
		}

		// 2. Qase token
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

		// 3. LLM provider
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
