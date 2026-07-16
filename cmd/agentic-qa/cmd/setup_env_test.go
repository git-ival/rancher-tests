package cmd

import (
	"testing"

	"github.com/rancher/tests/internal/agenticqa/types"
)

func TestFindSetupEnvironment(t *testing.T) {
	setup := types.SetupEnvironments{Groups: []types.SetupEnvironment{
		{Name: "daily", JenkinsJobs: []string{"job-a"}},
		{Name: "weekly", JenkinsJobs: []string{"job-b"}},
	}}
	group, err := findSetupEnvironment(setup, "job-b")
	if err != nil {
		t.Fatalf("findSetupEnvironment: %v", err)
	}
	if group.Name != "weekly" {
		t.Fatalf("group = %q", group.Name)
	}
}

func TestFindSetupEnvironmentRejectsAmbiguousJob(t *testing.T) {
	setup := types.SetupEnvironments{Groups: []types.SetupEnvironment{
		{Name: "a", JenkinsJobs: []string{"job"}},
		{Name: "b", JenkinsJobs: []string{"job"}},
	}}
	if _, err := findSetupEnvironment(setup, "job"); err == nil {
		t.Fatal("expected ambiguity error")
	}
}
