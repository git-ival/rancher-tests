package qase

import (
	"bytes"
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
	BaseURL = "https://api.qase.io/v1"

	// defaultHTTPTimeout is the timeout for individual Qase API HTTP requests.
	defaultHTTPTimeout = 30 * time.Second
	// defaultRateLimitWait is the wait duration when the Retry-After header is
	// missing from a 429 response.
	defaultRateLimitWait = 5 * time.Second
	// maxErrBodyLen is the maximum number of bytes of an API error body to
	// include in error messages.
	maxErrBodyLen = 500
	// qaseRetryAttempts is the number of attempts before giving up on a Qase
	// API call.
	qaseRetryAttempts = 5
	// qaseRetryInitialDelay is the initial delay between retry attempts.
	qaseRetryInitialDelay = 1 * time.Second
	// qaseRetryMaxDelay is the maximum delay between retry attempts.
	qaseRetryMaxDelay = 30 * time.Second
	// qasePaginationLimit is the maximum number of items per page when
	// listing Qase resources.
	qasePaginationLimit = 100
)

// TestResult represents a single test result from Qase.
type TestResult struct {
	CaseID  int    `json:"case_id"`
	Status  string `json:"status"`
	Comment string `json:"comment"`
	TimeMS  int    `json:"time_ms"`
	Hash    string `json:"hash"`
}

// AutomationTestNameFieldID is the workspace's default Qase field ID.
const AutomationTestNameFieldID = 15

// customFieldValue is the raw shape returned by the Qase v1 cases list endpoint.
type customFieldValue struct {
	ID    *int    `json:"id"`
	Value *string `json:"value"`
}

// TestCase represents a Qase test case.
type TestCase struct {
	ID           int                `json:"id"`
	Title        string             `json:"title"`
	SuiteID      *int               `json:"suite_id"`
	Priority     int                `json:"priority"`
	Automation   int                `json:"automation"`
	CustomFields []customFieldValue `json:"custom_fields"`
}

// AutomationTestName returns the value of the AutomationTestName custom field
// for this test case using the provided field ID, or an empty string if not set.
func (tc *TestCase) AutomationTestName(atnFieldID int) string {
	for _, cf := range tc.CustomFields {
		if cf.ID != nil && *cf.ID == atnFieldID && cf.Value != nil {
			return *cf.Value
		}
	}
	return ""
}

// Client wraps the Qase REST API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	atnFieldID int // AutomationTestName custom field ID (workspace-specific)
}

// NewClient creates a new Qase API client using the default ATN field ID.
func NewClient(token string) *Client {
	return NewClientWithATNFieldID(token, AutomationTestNameFieldID)
}

// NewClientWithATNFieldID creates a client with a custom automation-name field.
func NewClientWithATNFieldID(token string, atnFieldID int) *Client {
	return &Client{
		baseURL: BaseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		atnFieldID: atnFieldID,
	}
}

// apiResponse is the generic top-level Qase API response.
type apiResponse struct {
	Status bool            `json:"status"`
	Result json.RawMessage `json:"result"`
}

// paginatedResult holds the paginated list returned by Qase.
type paginatedResult struct {
	Entities json.RawMessage `json:"entities"`
	Total    int             `json:"total"`
	Count    int             `json:"count"`
}

// idResult is used for create endpoints that return an ID.
type idResult struct {
	ID int `json:"id"`
}

// doRequest retries rate limits and never logs response bodies.
func (c *Client) doRequest(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var resultBody json.RawMessage

	err := retry.Do(
		func() error {
			var reqBody io.Reader
			if body != nil {
				data, err := json.Marshal(body)
				if err != nil {
					return retry.Unrecoverable(fmt.Errorf("marshaling request body: %w", err))
				}
				reqBody = bytes.NewReader(data)
			}

			reqURL := c.baseURL + path
			req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
			if err != nil {
				return retry.Unrecoverable(fmt.Errorf("creating request: %w", err))
			}

			req.Header.Set("Token", c.token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")

			resp, err := c.httpClient.Do(req)
			if err != nil {
				return fmt.Errorf("executing request: %w", err)
			}
			defer resp.Body.Close()

			respData, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("reading response: %w", err)
			}

			if resp.StatusCode == http.StatusTooManyRequests {
				wait := defaultRateLimitWait
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if secs, parseErr := strconv.Atoi(ra); parseErr == nil {
						wait = time.Duration(secs) * time.Second
					}
				}
				logrus.WithField("retry_after_sec", wait.Seconds()).Warn("Qase API rate-limited, retrying")
				time.Sleep(wait)
				return fmt.Errorf("rate limited (HTTP 429)")
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				errBody := string(respData)
				if len(errBody) > maxErrBodyLen {
					errBody = errBody[:maxErrBodyLen] + "..."
				}
				logrus.WithFields(logrus.Fields{
					"status": resp.StatusCode,
					"path":   path,
					"body":   errBody,
				}).Error("Qase API request failed")
				return retry.Unrecoverable(fmt.Errorf("API error: HTTP %d: %s", resp.StatusCode, errBody))
			}

			if resp.StatusCode == http.StatusNoContent || len(respData) == 0 {
				return nil
			}

			var apiResp apiResponse
			if err := json.Unmarshal(respData, &apiResp); err != nil {
				return retry.Unrecoverable(fmt.Errorf("decoding API response: %w", err))
			}
			if !apiResp.Status {
				return retry.Unrecoverable(fmt.Errorf("API returned status=false"))
			}

			resultBody = apiResp.Result
			return nil
		},
		retry.Context(ctx),
		retry.Attempts(qaseRetryAttempts),
		retry.DelayType(retry.BackOffDelay),
		retry.Delay(qaseRetryInitialDelay),
		retry.MaxDelay(qaseRetryMaxDelay),
		retry.LastErrorOnly(true),
	)

	return resultBody, err
}

// CreateTestRun creates a new test run and returns its ID.
func (c *Client) CreateTestRun(ctx context.Context, project, title, description string) (int, error) {
	payload := map[string]any{
		"title":       title,
		"description": description,
	}
	result, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/run/%s", url.PathEscape(project)), payload)
	if err != nil {
		return 0, fmt.Errorf("creating test run: %w", err)
	}

	var id idResult
	if err := json.Unmarshal(result, &id); err != nil {
		return 0, fmt.Errorf("decoding test run ID: %w", err)
	}
	return id.ID, nil
}

// CreateTestRunWithCases creates a run with case IDs. If Qase exceeds its
// configuration limit, it creates an empty run for reporter-posted results.
func (c *Client) CreateTestRunWithCases(ctx context.Context, project, title, description string, caseIDs []int) (int, error) {
	if len(caseIDs) == 0 {
		return 0, fmt.Errorf("creating test run with cases: caseIDs cannot be empty")
	}

	payload := map[string]any{
		"title":       title,
		"description": description,
		"cases":       caseIDs,
	}

	result, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/run/%s", url.PathEscape(project)), payload)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 400") {
			logrus.Warnf("Qase project %s rejected run with %d cases (likely configuration combinations exceed limit); creating run without pre-populated cases", project, len(caseIDs))
			return c.CreateTestRun(ctx, project, title, description)
		}
		return 0, fmt.Errorf("creating test run with cases: %w", err)
	}

	var id idResult
	if err := json.Unmarshal(result, &id); err != nil {
		return 0, fmt.Errorf("decoding test run ID: %w", err)
	}
	return id.ID, nil
}

// DeleteTestRun deletes a test run.
func (c *Client) DeleteTestRun(ctx context.Context, project string, runID int) error {
	_, err := c.doRequest(ctx, http.MethodDelete, fmt.Sprintf("/run/%s/%d", url.PathEscape(project), runID), nil)
	if err != nil {
		return fmt.Errorf("deleting test run %d: %w", runID, err)
	}
	return nil
}

// CompleteTestRun marks a test run as complete.
func (c *Client) CompleteTestRun(ctx context.Context, project string, runID int) error {
	_, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/run/%s/%d/complete", url.PathEscape(project), runID), nil)
	if err != nil {
		return fmt.Errorf("completing test run %d: %w", runID, err)
	}
	return nil
}

// GetTestRunResults returns all results for a run (handles pagination).
func (c *Client) GetTestRunResults(ctx context.Context, project string, runID int) ([]TestResult, error) {
	var allResults []TestResult
	offset := 0

	for {
		path := fmt.Sprintf("/result/%s?run=%d&limit=%d&offset=%d",
			url.PathEscape(project), runID, qasePaginationLimit, offset)
		result, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("fetching test run results: %w", err)
		}

		var page paginatedResult
		if err := json.Unmarshal(result, &page); err != nil {
			return nil, fmt.Errorf("decoding paginated results: %w", err)
		}

		var results []TestResult
		if err := json.Unmarshal(page.Entities, &results); err != nil {
			return nil, fmt.Errorf("decoding test results: %w", err)
		}

		allResults = append(allResults, results...)
		if len(allResults) >= page.Total {
			break
		}
		offset += qasePaginationLimit
	}

	return allResults, nil
}

// CreateDefect creates a defect and returns its ID.
func (c *Client) CreateDefect(ctx context.Context, project, title, severity, actualResult string) (int, error) {
	payload := map[string]any{
		"title":         title,
		"severity":      severity,
		"actual_result": actualResult,
	}
	result, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/defect/%s", url.PathEscape(project)), payload)
	if err != nil {
		return 0, fmt.Errorf("creating defect: %w", err)
	}

	var id idResult
	if err := json.Unmarshal(result, &id); err != nil {
		return 0, fmt.Errorf("decoding defect ID: %w", err)
	}
	return id.ID, nil
}

// DeleteDefect deletes a defect.
func (c *Client) DeleteDefect(ctx context.Context, project string, defectID int) error {
	_, err := c.doRequest(ctx, http.MethodDelete, fmt.Sprintf("/defect/%s/%d", url.PathEscape(project), defectID), nil)
	if err != nil {
		return fmt.Errorf("deleting defect %d: %w", defectID, err)
	}
	return nil
}

// SearchCases searches test cases by title.
func (c *Client) SearchCases(ctx context.Context, project, query string) ([]TestCase, error) {
	path := fmt.Sprintf("/case/%s?search=%s&limit=%d",
		url.PathEscape(project), url.QueryEscape(query), qasePaginationLimit)
	result, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("searching cases: %w", err)
	}

	var page paginatedResult
	if err := json.Unmarshal(result, &page); err != nil {
		return nil, fmt.Errorf("decoding paginated cases: %w", err)
	}

	var cases []TestCase
	if err := json.Unmarshal(page.Entities, &cases); err != nil {
		return nil, fmt.Errorf("decoding test cases: %w", err)
	}
	return cases, nil
}

// GetTestHistory returns recent results for a specific test case.
func (c *Client) GetTestHistory(ctx context.Context, project string, caseID, limit int) ([]TestResult, error) {
	if limit <= 0 {
		limit = 10
	}
	path := fmt.Sprintf("/result/%s?case_id=%d&limit=%d",
		url.PathEscape(project), caseID, limit)
	result, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching test history for case %d: %w", caseID, err)
	}

	var page paginatedResult
	if err := json.Unmarshal(result, &page); err != nil {
		return nil, fmt.Errorf("decoding paginated history: %w", err)
	}

	var results []TestResult
	if err := json.Unmarshal(page.Entities, &results); err != nil {
		return nil, fmt.Errorf("decoding test history: %w", err)
	}
	return results, nil
}

// GetAutomationNameMap fetches all test cases for a project and returns a map of
// AutomationTestName (custom field 15) → case ID.
// Cases without the custom field are also indexed by their title as a fallback.
// Uses paginated requests to handle large projects.
func (c *Client) GetAutomationNameMap(ctx context.Context, project string) (map[string]int, error) {
	nameToID := map[string]int{}
	offset := 0

	for {
		path := fmt.Sprintf("/case/%s?limit=%d&offset=%d",
			url.PathEscape(project), qasePaginationLimit, offset)
		result, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("fetching cases for project %s: %w", project, err)
		}

		var page paginatedResult
		if err := json.Unmarshal(result, &page); err != nil {
			return nil, fmt.Errorf("decoding cases page: %w", err)
		}

		var cases []TestCase
		if err := json.Unmarshal(page.Entities, &cases); err != nil {
			return nil, fmt.Errorf("decoding test cases: %w", err)
		}

		for _, tc := range cases {
			if name := tc.AutomationTestName(c.atnFieldID); name != "" {
				nameToID[name] = tc.ID
			} else {
				// Fallback: index by title so schema-only lookups work too.
				nameToID[tc.Title] = tc.ID
			}
		}

		offset += len(cases)
		if offset >= page.Total {
			break
		}
	}

	return nameToID, nil
}

// GetTitleMap fetches all test cases for a project and returns a map of
// case title → case ID. Every case is indexed by its title, regardless of
// whether it has an AutomationTestName custom field. This is used by the
// test-function-name fallback when no schema-based mapping exists.
func (c *Client) GetTitleMap(ctx context.Context, project string) (map[string]int, error) {
	titleToID := map[string]int{}
	offset := 0

	for {
		path := fmt.Sprintf("/case/%s?limit=%d&offset=%d",
			url.PathEscape(project), qasePaginationLimit, offset)
		result, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("fetching cases for project %s: %w", project, err)
		}

		var page paginatedResult
		if err := json.Unmarshal(result, &page); err != nil {
			return nil, fmt.Errorf("decoding cases page: %w", err)
		}

		var cases []TestCase
		if err := json.Unmarshal(page.Entities, &cases); err != nil {
			return nil, fmt.Errorf("decoding test cases: %w", err)
		}

		for _, tc := range cases {
			titleToID[tc.Title] = tc.ID
		}

		offset += len(cases)
		if offset >= page.Total {
			break
		}
	}

	return titleToID, nil
}
