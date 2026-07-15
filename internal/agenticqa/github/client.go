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

const (
	// githubPageSize is the maximum number of items per page when listing
	// GitHub resources.
	githubPageSize = 100
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
	// Merged reports whether the PR has been merged into its base branch.
	Merged bool
	// Only meaningful once the PR is merged (or, for open PRs, once GitHub has computed a test-merge commit).
	MergeCommitSHA string
}

// Client wraps the GitHub API.
type Client struct {
	client *gh.Client
	token  string
}

// NewClient creates a GitHub client from a personal access token. An empty
// token yields an unauthenticated client (usable for public-repo reads at
// GitHub's lower unauthenticated rate limit).
func NewClient(token string) *Client {
	c := gh.NewClient(nil)
	if token != "" {
		c = c.WithAuthToken(token)
	}
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
	opts := &gh.ListOptions{PerPage: githubPageSize}

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
	if pr.Merged != nil {
		info.Merged = *pr.Merged
	}
	if pr.MergeCommitSHA != nil {
		info.MergeCommitSHA = *pr.MergeCommitSHA
	}

	logrus.Debugf("GetPRInfo: %s/%s#%d title=%q author=%s merged=%v", owner, repo, prNumber, info.Title, info.Author, info.Merged)
	return info, nil
}

// ListTags returns every tag name in the repository.
func (c *Client) ListTags(ctx context.Context, owner, repo string) ([]string, error) {
	var allTags []string
	opts := &gh.ListOptions{PerPage: githubPageSize}

	for {
		tags, resp, err := c.client.Repositories.ListTags(ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("listing tags: %w", err)
		}

		for _, t := range tags {
			if t.Name != nil {
				allTags = append(allTags, *t.Name)
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	logrus.Debugf("ListTags: %s/%s has %d tags", owner, repo, len(allTags))
	return allTags, nil
}

// CompareContainsCommit reports whether base contains head, i.e. head is an
// ancestor of (or identical to) base. It uses the GitHub compare API: a
// status of "identical" or "behind" (from head's perspective relative to
// base) means base already contains head's commit.
func (c *Client) CompareContainsCommit(ctx context.Context, owner, repo, base, head string) (bool, error) {
	comparison, _, err := c.client.Repositories.CompareCommits(ctx, owner, repo, base, head, nil)
	if err != nil {
		return false, fmt.Errorf("comparing %s...%s: %w", base, head, err)
	}

	status := comparison.GetStatus()
	contains := status == "identical" || status == "behind"
	logrus.Debugf("CompareContainsCommit: %s/%s %s...%s status=%s contains=%v", owner, repo, base, head, status, contains)
	return contains, nil
}

// GetBranchHeadSHA returns the current HEAD commit SHA of a branch.
func (c *Client) GetBranchHeadSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	ref, _, err := c.client.Git.GetRef(ctx, owner, repo, "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("getting ref for branch %q: %w", branch, err)
	}
	if ref.Object == nil || ref.Object.SHA == nil {
		return "", fmt.Errorf("branch %q ref has no SHA", branch)
	}
	return *ref.Object.SHA, nil
}

// ReleaseAsset describes a downloadable asset attached to a GitHub release.
type ReleaseAsset struct {
	Name               string
	BrowserDownloadURL string
}

// GetReleaseByTag returns the release notes body and asset list for the
// published release at the given tag. ok is false when the tag exists but has
// no published release (a bare git tag), which is common for non-GA/internal
// tags.
func (c *Client) GetReleaseByTag(ctx context.Context, owner, repo, tag string) (body string, assets []ReleaseAsset, ok bool, err error) {
	rel, resp, err := c.client.Repositories.GetReleaseByTag(ctx, owner, repo, tag)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return "", nil, false, nil
		}
		return "", nil, false, fmt.Errorf("getting release for tag %q: %w", tag, err)
	}

	if rel.Body != nil {
		body = *rel.Body
	}
	for _, a := range rel.Assets {
		var asset ReleaseAsset
		if a.Name != nil {
			asset.Name = *a.Name
		}
		if a.BrowserDownloadURL != nil {
			asset.BrowserDownloadURL = *a.BrowserDownloadURL
		}
		assets = append(assets, asset)
	}

	logrus.Debugf("GetReleaseByTag: %s/%s@%s body=%d bytes, %d asset(s)", owner, repo, tag, len(body), len(assets))
	return body, assets, true, nil
}

// BranchExists reports whether a branch exists in the repository.
func (c *Client) BranchExists(ctx context.Context, owner, repo, branch string) (bool, error) {
	_, resp, err := c.client.Repositories.GetBranch(ctx, owner, repo, branch, 0)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, fmt.Errorf("getting branch %q: %w", branch, err)
	}
	return true, nil
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
