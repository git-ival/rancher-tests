package cmd

import (
	"testing"

	"github.com/rancher/tests/internal/agenticqa/types"
)

func TestResolveJobQaseProjectPrefersIdentifiedOwnership(t *testing.T) {
	identified := types.IdentifiedTests{
		Tests:          []types.TestEntry{{QaseProjects: []string{"RM"}}},
		TestsByProject: map[string][]int{"RM": {0}},
	}
	mapping := map[string]any{"job_mappings": map[string]any{
		"job": map[string]any{"qase_project": "RANCHERINT"},
	}}

	project, err := resolveJobQaseProject("job", identified, mapping)
	if err != nil {
		t.Fatalf("resolveJobQaseProject: %v", err)
	}
	if project != "RM" {
		t.Fatalf("project = %q, want RM", project)
	}
}

func TestResolveJobQaseProjectUsesValidMappingForMultipleProjects(t *testing.T) {
	identified := types.IdentifiedTests{
		Tests: []types.TestEntry{
			{QaseProjects: []string{"RM"}},
			{QaseProjects: []string{"RRT"}},
		},
		TestsByProject: map[string][]int{"RM": {0}, "RRT": {1}},
	}
	mapping := map[string]any{"job_mappings": map[string]any{
		"job": map[string]any{"qase_project": "RRT"},
	}}

	project, err := resolveJobQaseProject("job", identified, mapping)
	if err != nil {
		t.Fatalf("resolveJobQaseProject: %v", err)
	}
	if project != "RRT" {
		t.Fatalf("project = %q, want RRT", project)
	}
}
