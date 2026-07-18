package jenkins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/avast/retry-go/v4"
	"github.com/sirupsen/logrus"
)

const (
	// jenkinsHTTPTimeout is the timeout for individual Jenkins API HTTP requests.
	jenkinsHTTPTimeout = 30 * time.Second
	// jenkinsRetryAttempts is the number of attempts before giving up on a
	// Jenkins API call.
	jenkinsRetryAttempts = 3
)

// Client wraps the Jenkins REST API.
type Client struct {
	baseURL    string
	httpClient *http.Client
	user       string
	token      string
	authHeader string
	crumbField string
	crumbValue string
}

// BuildStatus holds the result of a Jenkins build poll.
type BuildStatus struct {
	Result     string // "SUCCESS", "FAILURE", "UNSTABLE", "ABORTED", "IN_PROGRESS"
	DurationMS int64
	LogURL     string
}

// crumbResponse is the JSON shape returned by the Jenkins crumb issuer.
type crumbResponse struct {
	CrumbRequestField string `json:"crumbRequestField"`
	Crumb             string `json:"crumb"`
}

// queueResponse is the JSON shape for a Jenkins queue item.
type queueResponse struct {
	Executable struct {
		Number int `json:"number"`
	} `json:"executable"`
}

// buildResponse is the JSON shape for a Jenkins build.
type buildResponse struct {
	Result   *string `json:"result"`
	Duration int64   `json:"duration"`
	URL      string  `json:"url"`
	Building bool    `json:"building"`
}

type whoAmIResponse struct {
	Authenticated bool   `json:"authenticated"`
	Name          string `json:"name"`
}

// NewClient creates a Jenkins client.
// It attempts to fetch a CSRF crumb; errors are logged but not fatal
// (CSRF protection may be disabled on the instance).
func NewClient(baseURL, user, token string) *Client {
	token, authHeader := normalizeCredential(token)
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: jenkinsHTTPTimeout},
		user:       user,
		token:      token,
		authHeader: authHeader,
	}
	c.fetchCrumb()
	return c
}

func normalizeCredential(token string) (string, string) {
	token = strings.TrimSpace(token)
	if len(token) >= len("Basic ") && strings.EqualFold(token[:len("Basic ")], "Basic ") {
		token = strings.TrimSpace(token[len("Basic "):])
	}

	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(token)
	}
	if err != nil || len(decoded) == 0 || !utf8.Valid(decoded) {
		return token, ""
	}
	for _, r := range string(decoded) {
		if r < 0x20 || r > 0x7e {
			return token, ""
		}
	}
	if strings.ContainsRune(string(decoded), ':') {
		return "", "Basic " + base64.StdEncoding.EncodeToString(decoded)
	}
	return string(decoded), ""
}

func (c *Client) setAuth(req *http.Request) {
	if c.authHeader != "" {
		req.Header.Set("Authorization", c.authHeader)
		return
	}
	req.SetBasicAuth(c.user, c.token)
}

// fetchCrumb attempts to retrieve the Jenkins CSRF crumb.
func (c *Client) fetchCrumb() {
	crumbURL := c.baseURL + "/crumbIssuer/api/json"

	req, err := http.NewRequest(http.MethodGet, crumbURL, nil)
	if err != nil {
		logrus.Debugf("jenkins: failed to build crumb request: %v", err)
		return
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logrus.Debugf("jenkins: crumb fetch failed (CSRF may be disabled): %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logrus.Debugf("jenkins: crumb endpoint returned status %d (CSRF may be disabled)", resp.StatusCode)
		return
	}

	var cr crumbResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		logrus.Debugf("jenkins: failed to decode crumb response: %v", err)
		return
	}

	c.crumbField = cr.CrumbRequestField
	c.crumbValue = cr.Crumb
	logrus.Debugf("jenkins: fetched CSRF crumb (field=%s)", c.crumbField)
}

// ValidateAuth verifies that Jenkins recognizes the configured credentials.
func (c *Client) ValidateAuth(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/whoAmI/api/json", nil)
	if err != nil {
		return "", fmt.Errorf("building authentication request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("checking authentication: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("authentication returned status %d", resp.StatusCode)
	}

	var who whoAmIResponse
	if err := json.NewDecoder(resp.Body).Decode(&who); err != nil {
		return "", fmt.Errorf("decoding authentication response: %w", err)
	}
	if !who.Authenticated {
		return "", fmt.Errorf("Jenkins reported an anonymous session")
	}
	return who.Name, nil
}

// TriggerBuild triggers a parameterized build and returns the queue item ID.
func (c *Client) TriggerBuild(ctx context.Context, folder, jobName string, params map[string]string) (int, error) {
	buildURL := fmt.Sprintf("%s/job/%s/job/%s/buildWithParameters", c.baseURL, folder, jobName)

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	var queueID int
	err := retry.Do(func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildURL, strings.NewReader(form.Encode()))
		if err != nil {
			return fmt.Errorf("building trigger request: %w", err)
		}

		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c.setAuth(req)
		if c.crumbField != "" {
			req.Header.Set(c.crumbField, c.crumbValue)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("sending trigger request: %w", err)
		}
		defer resp.Body.Close()
		// Drain the body to allow connection reuse; never log contents.
		_, _ = io.Copy(io.Discard, resp.Body)

		logrus.Debugf("jenkins: TriggerBuild %s/%s status=%d content-type=%s",
			folder, jobName, resp.StatusCode, resp.Header.Get("Content-Type"))

		if resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("trigger build returned status %d", resp.StatusCode)
		}

		location := resp.Header.Get("Location")
		if location == "" {
			return fmt.Errorf("trigger build response missing Location header")
		}

		id, err := parseQueueID(location)
		if err != nil {
			return fmt.Errorf("parsing queue ID from Location header: %w", err)
		}
		queueID = id
		return nil
	},
		retry.Attempts(jenkinsRetryAttempts),
		retry.Context(ctx),
		retry.LastErrorOnly(true),
	)
	if err != nil {
		return 0, fmt.Errorf("triggering build %s/%s: %w", folder, jobName, err)
	}

	logrus.Infof("jenkins: triggered %s/%s, queue ID=%d", folder, jobName, queueID)
	return queueID, nil
}

// parseQueueID extracts the queue item ID from a Jenkins Location header.
// The header typically looks like: http://jenkins.example.com/queue/item/123/
func parseQueueID(location string) (int, error) {
	location = strings.TrimRight(location, "/")
	parts := strings.Split(location, "/")
	if len(parts) == 0 {
		return 0, fmt.Errorf("empty location")
	}
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, fmt.Errorf("non-numeric queue ID in %q: %w", location, err)
	}
	return id, nil
}

// GetQueueBuildNumber polls the queue item until it starts and returns the build number.
func (c *Client) GetQueueBuildNumber(ctx context.Context, queueID int) (int, error) {
	queueURL := fmt.Sprintf("%s/queue/item/%d/api/json", c.baseURL, queueID)

	var buildNumber int
	err := retry.Do(func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, queueURL, nil)
		if err != nil {
			return fmt.Errorf("building queue request: %w", err)
		}
		c.setAuth(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("fetching queue item: %w", err)
		}
		defer resp.Body.Close()

		logrus.Debugf("jenkins: GetQueueBuildNumber queue=%d status=%d content-type=%s",
			queueID, resp.StatusCode, resp.Header.Get("Content-Type"))

		if resp.StatusCode != http.StatusOK {
			// Drain and discard body; never log contents.
			_, _ = io.Copy(io.Discard, resp.Body)
			return fmt.Errorf("queue item returned status %d", resp.StatusCode)
		}

		var qr queueResponse
		if err := json.NewDecoder(resp.Body).Decode(&qr); err != nil {
			return fmt.Errorf("decoding queue response: %w", err)
		}

		if qr.Executable.Number == 0 {
			return fmt.Errorf("build not yet started for queue item %d", queueID)
		}

		buildNumber = qr.Executable.Number
		return nil
	},
		retry.Attempts(jenkinsRetryAttempts),
		retry.Context(ctx),
		retry.LastErrorOnly(true),
	)
	if err != nil {
		return 0, fmt.Errorf("getting build number for queue %d: %w", queueID, err)
	}

	logrus.Infof("jenkins: queue %d → build #%d", queueID, buildNumber)
	return buildNumber, nil
}

// GetBuildStatus returns the current status of a build.
func (c *Client) GetBuildStatus(ctx context.Context, folder, jobName string, buildNumber int) (*BuildStatus, error) {
	buildURL := fmt.Sprintf("%s/job/%s/job/%s/%d/api/json", c.baseURL, folder, jobName, buildNumber)

	var status *BuildStatus
	err := retry.Do(func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildURL, nil)
		if err != nil {
			return fmt.Errorf("building build status request: %w", err)
		}
		c.setAuth(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("fetching build status: %w", err)
		}
		defer resp.Body.Close()

		logrus.Debugf("jenkins: GetBuildStatus %s/%s#%d status=%d content-type=%s",
			folder, jobName, buildNumber, resp.StatusCode, resp.Header.Get("Content-Type"))

		if resp.StatusCode != http.StatusOK {
			// Drain and discard body; never log contents.
			_, _ = io.Copy(io.Discard, resp.Body)
			return fmt.Errorf("build status returned HTTP %d", resp.StatusCode)
		}

		var br buildResponse
		if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
			return fmt.Errorf("decoding build response: %w", err)
		}

		result := "IN_PROGRESS"
		if !br.Building && br.Result != nil {
			result = *br.Result
		}

		logURL := fmt.Sprintf("%s/job/%s/job/%s/%d/console", c.baseURL, folder, jobName, buildNumber)

		status = &BuildStatus{
			Result:     result,
			DurationMS: br.Duration,
			LogURL:     logURL,
		}
		return nil
	},
		retry.Attempts(jenkinsRetryAttempts),
		retry.Context(ctx),
		retry.LastErrorOnly(true),
	)
	if err != nil {
		return nil, fmt.Errorf("getting build status %s/%s#%d: %w", folder, jobName, buildNumber, err)
	}

	logrus.Debugf("jenkins: %s/%s#%d result=%s duration=%dms", folder, jobName, buildNumber, status.Result, status.DurationMS)
	return status, nil
}

// ValidateJenkinsfile sends a Declarative Pipeline script to the Jenkins
// pipeline-model-converter linter. Returns nil when the script is valid,
// or an error containing the linter's human-readable diagnostics.
//
// The endpoint is POST /pipeline-model-converter/validate with a multipart
// form field named "jenkinsfile". It responds with a plain-text "ok" or an
// error message; HTTP 200 is returned in both cases.
func (c *Client) ValidateJenkinsfile(ctx context.Context, script string) error {
	validateURL := c.baseURL + "/pipeline-model-converter/validate"

	var buf strings.Builder
	boundary := "agentic-qa-boundary"
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"jenkinsfile\"\r\n\r\n")
	buf.WriteString(script)
	buf.WriteString("\r\n--" + boundary + "--\r\n")
	body := buf.String()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, validateURL, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("building lint request: %w", err)
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	c.setAuth(req)
	if c.crumbField != "" {
		req.Header.Set(c.crumbField, c.crumbValue)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending lint request: %w", err)
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	text := strings.TrimSpace(string(responseBody))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linter returned HTTP %d: %s", resp.StatusCode, text)
	}
	if strings.ToLower(text) != "ok" {
		return fmt.Errorf("Jenkinsfile validation failed:\n%s", text)
	}
	return nil
}

// UpdateBuild labels a Jenkins build for Agentic QA tracking.
func (c *Client) UpdateBuild(ctx context.Context, folder, jobName string, buildNumber int, displayName, description string) error {
	buildURL := fmt.Sprintf("%s/job/%s/job/%s/%d/configSubmit", c.baseURL, folder, jobName, buildNumber)
	payload, err := json.Marshal(map[string]string{"displayName": displayName, "description": description})
	if err != nil {
		return fmt.Errorf("encoding build metadata: %w", err)
	}
	form := url.Values{"json": []string{string(payload)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("building metadata request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setAuth(req)
	if c.crumbField != "" {
		req.Header.Set(c.crumbField, c.crumbValue)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("updating build metadata: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("updating build metadata returned status %d", resp.StatusCode)
	}
	return nil
}
