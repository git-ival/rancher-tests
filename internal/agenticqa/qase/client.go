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
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/sirupsen/logrus"
)

const BaseURL = "https://api.qase.io/v1"

// TestResult represents a single test result from Qase.
type TestResult struct {
	CaseID  int    `json:"case_id"`
	Status  string `json:"status"`
	Comment string `json:"comment"`
	TimeMS  int    `json:"time_ms"`
	Hash    string `json:"hash"`
}

// TestCase represents a Qase test case.
type TestCase struct {
	ID         int    `json:"id"`
	Title      string `json:"title"`
	SuiteID    *int   `json:"suite_id"`
	Priority   int    `json:"priority"`
	Automation int    `json:"automation"`
}

// Client wraps the Qase REST API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a new Qase API client.
func NewClient(token string) *Client {
	return &Client{
		baseURL: BaseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
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

// doRequest executes an HTTP request with retry on 429 (rate-limit).
// SECURITY: response bodies are never logged.
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
				wait := 5 * time.Second
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
				logrus.WithField("status", resp.StatusCode).Error("Qase API request failed")
				return retry.Unrecoverable(fmt.Errorf("API error: HTTP %d", resp.StatusCode))
			}

			// For 204 No Content (e.g. DELETE), there is no body to parse.
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
		retry.Attempts(5),
		retry.DelayType(retry.BackOffDelay),
		retry.Delay(1*time.Second),
		retry.MaxDelay(30*time.Second),
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
	limit := 100

	for {
		path := fmt.Sprintf("/result/%s?run=%d&limit=%d&offset=%d",
			url.PathEscape(project), runID, limit, offset)
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
		offset += limit
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
	path := fmt.Sprintf("/case/%s?search=%s&limit=100",
		url.PathEscape(project), url.QueryEscape(query))
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
