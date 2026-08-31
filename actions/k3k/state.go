package k3k

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Stage string

const (
	StagePending         Stage = "pending"
	StageK3KCreating     Stage = "k3k-creating"
	StageK3KReady        Stage = "k3k-ready"
	StageRancherCreating Stage = "rancher-creating"
	StageRancherCreated  Stage = "rancher-created"
	StageRegistering     Stage = "registering"
	StageRegistered      Stage = "registered"
	StageDeletingRancher Stage = "deleting-rancher"
	StageDeletingK3K     Stage = "deleting-k3k"
	StageDeleted         Stage = "deleted"
	StageFailed          Stage = "failed"
)

type ClusterState struct {
	Name string `json:"name"`
	// Target is the Rancher display name of the host cluster selected for this k3k cluster.
	Target      string `json:"target"`
	TargetType  string `json:"targetType,omitempty"`
	Namespace   string `json:"namespace"`
	ArtifactDir string `json:"artifactDir"`
	Kubeconfig  string `json:"kubeconfig"`
	// RancherProvisioningID is namespace/name; RancherManagementID is the generated c-m-* ID.
	RancherProvisioningID string `json:"rancherProvisioningId,omitempty"`
	RancherManagementID   string `json:"rancherManagementId,omitempty"`
	Stage                 Stage  `json:"stage"`
	// ResumeStage records the stage that failed so a rerun can skip completed lifecycle work.
	ResumeStage Stage     `json:"resumeStage,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type BatchState struct {
	Batch    string                   `json:"batch"`
	Clusters map[string]*ClusterState `json:"clusters"`

	path string
	mu   sync.Mutex
}

func LoadOrCreateState(config *Config) (*BatchState, error) {
	state := &BatchState{Batch: config.Batch, Clusters: map[string]*ClusterState{}, path: config.StateFile}
	data, err := os.ReadFile(config.StateFile)
	if err == nil {
		if err := json.Unmarshal(data, state); err != nil {
			return nil, fmt.Errorf("parse state: %w", err)
		}
		state.path = config.StateFile
		if state.Batch != config.Batch {
			return nil, fmt.Errorf("state belongs to batch %q", state.Batch)
		}
		resolvedTargets := map[string]bool{}
		for _, target := range config.Targets {
			resolvedTargets[target.Name] = true
		}
		for _, cluster := range state.Clusters {
			if !resolvedTargets[cluster.Target] {
				return nil, fmt.Errorf("state cluster %q references target %q that is not currently resolved", cluster.Name, cluster.Target)
			}
		}
		return state, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	targets := weightedTargets(config.Targets)
	for i := 0; i < config.Count; i++ {
		name := fmt.Sprintf("%s-%03d", config.Batch, i+1)
		artifactDir := filepath.Join(config.ArtifactsDir, config.Batch, name)
		namespace := "k3k-" + name
		state.Clusters[name] = &ClusterState{
			Name: name, Target: targets[i%len(targets)].Name, TargetType: targets[i%len(targets)].Type, Namespace: namespace,
			ArtifactDir:           artifactDir,
			Kubeconfig:            filepath.Join(artifactDir, namespace+"-"+name+"-kubeconfig.yaml"),
			RancherProvisioningID: config.Rancher.Workspace + "/" + name,
			Stage:                 StagePending, UpdatedAt: time.Now().UTC(),
		}
	}
	return state, state.Save()
}

func weightedTargets(targets []Target) []Target {
	var weighted []Target
	for _, target := range targets {
		for range target.Weight {
			weighted = append(weighted, target)
		}
	}
	return weighted
}

func (s *BatchState) Update(name string, stage Stage, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.Clusters[name]
	if stage == StageFailed {
		item.ResumeStage = item.Stage
	} else {
		item.ResumeStage = ""
	}
	item.Stage = stage
	item.UpdatedAt = time.Now().UTC()
	item.LastError = ""
	if err != nil {
		item.LastError = err.Error()
	}
	return s.saveLocked()
}

func (s *BatchState) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *BatchState) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
