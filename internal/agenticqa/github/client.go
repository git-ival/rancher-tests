package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v68/github"
	"github.com/sirupsen/logrus"
)

// PRInfo holds pull request metadata.
type PRInfo struct {
	Title  string
	Body   string
	Author string
	Branch string
	Base   string
	Number int
	URL    string
}

// Client wraps the GitHub API.
type Client struct {
	client *gh.Client
	token  string
}

// NewClient creates a GitHub client from a personal access token.
func NewClient(token string) *Client {
	c := gh.NewClient(nil).WithAuthToken(token)
	return &Client{
		client: c,
		token:  token,
	}
}

// ParseRepo splits "owner/repo" into its two components.
func ParseRepo(fullRepo string) (owner, repo string, err error) {
	parts := strings.SplitN(fullRepo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo format %q: expected owner/repo", fullRepo)
	}
	return parts[0], parts[1], nil
}

// GetPRDiff returns the unified diff text for a PR.
// Uses raw HTTP because go-github doesn't directly expose the diff media type.
func (c *Client) GetPRDiff(ctx context.Context, owner, repo string, prNumber int) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating diff request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3.diff")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching diff: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("diff request returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading diff body: %w", err)
	}

	logrus.Debugf("GetPRDiff: fetched diff for %s/%s#%d (%d bytes)", owner, repo, prNumber, len(body))
	return string(body), nil
}

// GetPRFiles returns the list of file paths changed in a PR.
func (c *Client) GetPRFiles(ctx context.Context, owner, repo string, prNumber int) ([]string, error) {
	var allFiles []string
	opts := &gh.ListOptions{PerPage: 100}

	for {
		files, resp, err := c.client.PullRequests.ListFiles(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return nil, fmt.Errorf("listing PR files: %w", err)
		}

		for _, f := range files {
			if f.Filename != nil {
				allFiles = append(allFiles, *f.Filename)
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	logrus.Debugf("GetPRFiles: %s/%s#%d has %d changed files", owner, repo, prNumber, len(allFiles))
	return allFiles, nil
}

// GetPRInfo returns metadata for a PR.
func (c *Client) GetPRInfo(ctx context.Context, owner, repo string, prNumber int) (*PRInfo, error) {
	pr, _, err := c.client.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("getting PR info: %w", err)
	}

	info := &PRInfo{
		Number: prNumber,
	}

	if pr.Title != nil {
		info.Title = *pr.Title
	}
	if pr.Body != nil {
		info.Body = *pr.Body
	}
	if pr.User != nil && pr.User.Login != nil {
		info.Author = *pr.User.Login
	}
	if pr.Head != nil && pr.Head.Ref != nil {
		info.Branch = *pr.Head.Ref
	}
	if pr.Base != nil && pr.Base.Ref != nil {
		info.Base = *pr.Base.Ref
	}
	if pr.HTMLURL != nil {
		info.URL = *pr.HTMLURL
	}

	logrus.Debugf("GetPRInfo: %s/%s#%d title=%q author=%s", owner, repo, prNumber, info.Title, info.Author)
	return info, nil
}

// CreateIssue creates a GitHub issue and returns its HTML URL.
func (c *Client) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error) {
	issueReq := &gh.IssueRequest{
		Title:  gh.Ptr(title),
		Body:   gh.Ptr(body),
		Labels: &labels,
	}

	issue, _, err := c.client.Issues.Create(ctx, owner, repo, issueReq)
	if err != nil {
		return "", fmt.Errorf("creating issue: %w", err)
	}

	url := issue.GetHTMLURL()
	logrus.Infof("CreateIssue: created %s", url)
	return url, nil
}

// CreatePR creates a pull request and returns its HTML URL.
// Set draft=true to open it as a draft PR (useful for local testing).
func (c *Client) CreatePR(ctx context.Context, owner, repo, branch, base, title, body string, draft bool) (string, error) {
	newPR := &gh.NewPullRequest{
		Title: gh.Ptr(title),
		Head:  gh.Ptr(branch),
		Base:  gh.Ptr(base),
		Body:  gh.Ptr(body),
		Draft: gh.Ptr(draft),
	}

	pr, _, err := c.client.PullRequests.Create(ctx, owner, repo, newPR)
	if err != nil {
		return "", fmt.Errorf("creating PR: %w", err)
	}

	url := pr.GetHTMLURL()
	logrus.Infof("CreatePR: created %s", url)
	return url, nil
}

// PostComment posts a comment on a PR/issue.
func (c *Client) PostComment(ctx context.Context, owner, repo string, number int, body string) error {
	comment := &gh.IssueComment{
		Body: gh.Ptr(body),
	}

	_, _, err := c.client.Issues.CreateComment(ctx, owner, repo, number, comment)
	if err != nil {
		return fmt.Errorf("posting comment on %s/%s#%d: %w", owner, repo, number, err)
	}

	logrus.Debugf("PostComment: commented on %s/%s#%d", owner, repo, number)
	return nil
}

// CloseIssue closes a GitHub issue.
func (c *Client) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	_, _, err := c.client.Issues.Edit(ctx, owner, repo, number, &gh.IssueRequest{
		State: gh.Ptr("closed"),
	})
	if err != nil {
		return fmt.Errorf("closing issue %s/%s#%d: %w", owner, repo, number, err)
	}

	logrus.Infof("CloseIssue: closed %s/%s#%d", owner, repo, number)
	return nil
}

// ClosePR closes a GitHub PR.
func (c *Client) ClosePR(ctx context.Context, owner, repo string, number int) error {
	_, _, err := c.client.PullRequests.Edit(ctx, owner, repo, number, &gh.PullRequest{
		State: gh.Ptr("closed"),
	})
	if err != nil {
		return fmt.Errorf("closing PR %s/%s#%d: %w", owner, repo, number, err)
	}

	logrus.Infof("ClosePR: closed %s/%s#%d", owner, repo, number)
	return nil
}

// AssignCopilot assigns a coding-agent bot user to an issue.
// username is the GitHub login of the bot (e.g. "copilot-swe-agent"), taken
// from PipelineEnv.CopilotUsername so it is not hardcoded in the binary.
func (c *Client) AssignCopilot(ctx context.Context, owner, repo string, issueNumber int, copilotToken, username string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/assignees", owner, repo, issueNumber)

	payload := fmt.Sprintf(`{"assignees":[%q]}`, username)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating assign request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+copilotToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("assigning copilot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("assign copilot returned status %d", resp.StatusCode)
	}

	logrus.Infof("AssignCopilot: assigned %s to %s/%s#%d", username, owner, repo, issueNumber)
	return nil
}
