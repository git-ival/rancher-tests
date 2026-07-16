package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// PipelineState tracks all resources created by the pipeline for cleanup.
type PipelineState struct {
	QaseRuns     []QaseRunRef     `json:"qase_runs"`
	QaseDefects  []QaseDefectRef  `json:"qase_defects"`
	GithubIssues []GithubIssueRef `json:"github_issues"`
	GithubPRs    []GithubPRRef    `json:"github_prs"`
}

// QaseRunRef identifies a Qase test run.
type QaseRunRef struct {
	Project string `json:"project"`
	RunID   int    `json:"run_id"`
}

// QaseDefectRef identifies a Qase defect.
type QaseDefectRef struct {
	Project  string `json:"project"`
	DefectID int    `json:"defect_id"`
}

// GithubIssueRef identifies a GitHub issue.
type GithubIssueRef struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
}

// GithubPRRef identifies a GitHub pull request.
type GithubPRRef struct {
	Repo     string `json:"repo"`
	PRNumber int    `json:"pr_number"`
}

// Tracker reads/writes pipeline_state.json.
type Tracker struct {
	path string
	mu   sync.Mutex
}

// NewTracker creates a new Tracker that persists state at the given path.
func NewTracker(path string) *Tracker {
	return &Tracker{path: path}
}

// Load reads the pipeline state from disk.
func (t *Tracker) Load() (*PipelineState, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := os.ReadFile(t.path)
	if err != nil {
		return nil, fmt.Errorf("reading pipeline state from %s: %w", t.path, err)
	}

	var s PipelineState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshalling pipeline state: %w", err)
	}

	return &s, nil
}

// Save writes the pipeline state to disk atomically.
func (t *Tracker) Save(s *PipelineState) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.saveUnsafe(s)
}

// saveUnsafe writes state to disk without acquiring the mutex.
// Callers must hold t.mu.
func (t *Tracker) saveUnsafe(s *PipelineState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling pipeline state: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(t.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating pipeline state directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "pipeline_state_*.json.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file for pipeline state: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp pipeline state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp pipeline state: %w", err)
	}

	if err := os.Rename(tmpName, t.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming temp pipeline state to %s: %w", t.path, err)
	}

	return nil
}

// Init writes an empty pipeline state to disk.
func (t *Tracker) Init() error {
	s := &PipelineState{
		QaseRuns:     []QaseRunRef{},
		QaseDefects:  []QaseDefectRef{},
		GithubIssues: []GithubIssueRef{},
		GithubPRs:    []GithubPRRef{},
	}
	return t.Save(s)
}

// Ensure creates empty state when missing.
func (t *Tracker) Ensure() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, err := os.Stat(t.path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking pipeline state %s: %w", t.path, err)
	}

	return t.saveUnsafe(&PipelineState{
		QaseRuns:     []QaseRunRef{},
		QaseDefects:  []QaseDefectRef{},
		GithubIssues: []GithubIssueRef{},
		GithubPRs:    []GithubPRRef{},
	})
}

// AddQaseRun atomically appends a Qase run reference to the state.
func (t *Tracker) AddQaseRun(project string, runID int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	s, err := t.loadUnsafe()
	if err != nil {
		return err
	}

	s.QaseRuns = append(s.QaseRuns, QaseRunRef{Project: project, RunID: runID})
	return t.saveUnsafe(s)
}

// AddQaseDefect atomically appends a Qase defect reference to the state.
func (t *Tracker) AddQaseDefect(project string, defectID int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	s, err := t.loadUnsafe()
	if err != nil {
		return err
	}

	s.QaseDefects = append(s.QaseDefects, QaseDefectRef{Project: project, DefectID: defectID})
	return t.saveUnsafe(s)
}

// AddGithubIssue atomically appends a GitHub issue reference to the state.
func (t *Tracker) AddGithubIssue(repo string, issueNumber int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	s, err := t.loadUnsafe()
	if err != nil {
		return err
	}

	s.GithubIssues = append(s.GithubIssues, GithubIssueRef{Repo: repo, IssueNumber: issueNumber})
	return t.saveUnsafe(s)
}

// AddGithubPR atomically appends a GitHub PR reference to the state.
func (t *Tracker) AddGithubPR(repo string, prNumber int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	s, err := t.loadUnsafe()
	if err != nil {
		return err
	}

	s.GithubPRs = append(s.GithubPRs, GithubPRRef{Repo: repo, PRNumber: prNumber})
	return t.saveUnsafe(s)
}

// loadUnsafe reads state from disk without acquiring the mutex.
// Callers must hold t.mu.
func (t *Tracker) loadUnsafe() (*PipelineState, error) {
	data, err := os.ReadFile(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PipelineState{
				QaseRuns:     []QaseRunRef{},
				QaseDefects:  []QaseDefectRef{},
				GithubIssues: []GithubIssueRef{},
				GithubPRs:    []GithubPRRef{},
			}, nil
		}
		return nil, fmt.Errorf("reading pipeline state from %s: %w", t.path, err)
	}

	var s PipelineState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshalling pipeline state: %w", err)
	}

	return &s, nil
}
