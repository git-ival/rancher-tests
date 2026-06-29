package jenkins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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

// NewClient creates a Jenkins client.
// It attempts to fetch a CSRF crumb; errors are logged but not fatal
// (CSRF protection may be disabled on the instance).
func NewClient(baseURL, user, token string) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: jenkinsHTTPTimeout},
		user:       user,
		token:      token,
	}
	c.fetchCrumb()
	return c
}

// fetchCrumb attempts to retrieve the Jenkins CSRF crumb.
func (c *Client) fetchCrumb() {
	crumbURL := c.baseURL + "/crumbIssuer/api/json"

	req, err := http.NewRequest(http.MethodGet, crumbURL, nil)
	if err != nil {
		logrus.Debugf("jenkins: failed to build crumb request: %v", err)
		return
	}
	req.SetBasicAuth(c.user, c.token)

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
		req.SetBasicAuth(c.user, c.token)
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
		req.SetBasicAuth(c.user, c.token)

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
		req.SetBasicAuth(c.user, c.token)

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
