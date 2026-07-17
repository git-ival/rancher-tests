package cmd

import (
	"testing"

	"github.com/rancher/tests/internal/agenticqa/runconfig"
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

func TestBuildTriggerRuntimeQuotesTestRegex(t *testing.T) {
	identified := types.IdentifiedTests{Tests: []types.TestEntry{{Functions: []string{"TestOne", "TestTwo"}, QaseProjects: []string{"RM"}}}, TestsByProject: map[string][]int{"RM": {0}}}
	mapping := map[string]any{"job_mappings": map[string]any{"job": map[string]any{
		"parameters": map[string]any{
			"QASE_TEST_RUN_ID": map[string]any{},
			"GOTEST_TESTCASE":  map[string]any{},
			"TEST_PACKAGE":     map[string]any{},
		},
	}}}
	runtime, err := buildTriggerRuntime("job", identified, mapping, nil, "")
	if err != nil {
		t.Fatalf("buildTriggerRuntime: %v", err)
	}
	if got := runtime.Parameters["GOTEST_TESTCASE"]; got != `-run ^\(TestOne\|TestTwo\)$` {
		t.Fatalf("GOTEST_TESTCASE = %q", got)
	}
	if got := runtime.Parameters["TEST_PACKAGE"]; got != "..." {
		t.Fatalf("TEST_PACKAGE = %q", got)
	}
}

func TestTestPackageSelector(t *testing.T) {
	tests := []struct {
		parameter string
		dirs      []string
		want      string
	}{
		{"TEST_PACKAGE", []string{"validation/rbac"}, "rbac/..."},
		{"TEST_PACKAGE", []string{"validation/rbac", "validation/hostedtenant/rbac"}, "..."},
		{"GO_TEST_PACKAGE", []string{"validation/rbac"}, "./validation/rbac/..."},
		{"GO_TEST_PACKAGE", []string{"validation/rbac", "validation/hostedtenant/rbac"}, "./validation/..."},
	}
	for _, test := range tests {
		if got := testPackageSelector(test.parameter, test.dirs); got != test.want {
			t.Errorf("testPackageSelector(%q, %v) = %q, want %q", test.parameter, test.dirs, got, test.want)
		}
	}
}

func TestBuildEnvironmentJobRuntimeUsesProvisionerDefaults(t *testing.T) {
	parameters := map[string]any{}
	for _, name := range []string{"TERRAFORM_CONFIG", "ANSIBLE_VARIABLES", "CATTLE_TEST_CONFIG", "TEST_PACKAGE", "GOTEST_TESTCASE_VALIDATION", "VALIDATION_TEST_TAGS", "TEST_TIMEOUT", "INDIVIDUAL_JOB", "HOSTNAME_PREFIX"} {
		parameters[name] = map[string]any{"default": "default-" + name}
	}
	mapping := map[string]any{"job_mappings": map[string]any{"env-job": map[string]any{
		"folder": "rancher_qa", "qase_project": "RM", "parameters": parameters,
	}}}
	identified := types.IdentifiedTests{
		PRNumber: 42, Repo: "rancher/tests", BaseRef: "main",
		Tests:          []types.TestEntry{{Functions: []string{"TestOne"}, BuildTags: []string{"validation"}, QaseProjects: []string{"RM"}}},
		TestsByProject: map[string][]int{"RM": {0}},
	}
	setup := &types.SetupEnvironments{Groups: []types.SetupEnvironment{{Name: "all"}}}
	runtime, err := buildEnvironmentJobRuntime("env-job", identified, mapping, setup, "3h")
	if err != nil {
		t.Fatalf("buildEnvironmentJobRuntime: %v", err)
	}
	if runtime.Parameters["TERRAFORM_CONFIG"] != "default-TERRAFORM_CONFIG" {
		t.Fatalf("terraform config = %q", runtime.Parameters["TERRAFORM_CONFIG"])
	}
	if runtime.Parameters["GOTEST_TESTCASE_VALIDATION"] != `-run ^\(TestOne\)$` {
		t.Fatalf("test case = %q", runtime.Parameters["GOTEST_TESTCASE_VALIDATION"])
	}
	if runtime.Parameters["TEST_PACKAGE"] != "./validation/..." || runtime.Parameters["TEST_TIMEOUT"] != "3h" {
		t.Fatalf("runtime parameters = %#v", runtime.Parameters)
	}
}

func TestBuildEnvironmentJobRuntimeUsesConfiguredQAInfra(t *testing.T) {
	parameters := map[string]any{}
	for _, name := range []string{"TERRAFORM_CONFIG", "ANSIBLE_VARIABLES", "CATTLE_TEST_CONFIG", "TEST_PACKAGE", "GOTEST_TESTCASE_VALIDATION", "VALIDATION_TEST_TAGS", "TEST_TIMEOUT", "INDIVIDUAL_JOB", "HOSTNAME_PREFIX", "QA_INFRA_REPO_URL", "QA_INFRA_BRANCH"} {
		parameters[name] = map[string]any{"default": "default"}
	}
	mapping := map[string]any{"job_mappings": map[string]any{"env-job": map[string]any{"qase_project": "RM", "parameters": parameters}}}
	identified := types.IdentifiedTests{PRNumber: 42, Tests: []types.TestEntry{{QaseProjects: []string{"RM"}}}, TestsByProject: map[string][]int{"RM": {0}}}
	runConfig = &runconfig.Config{QAInfra: runconfig.QAInfraConfig{RepoURL: "https://example.test/infra", Branch: "release"}}
	t.Cleanup(func() { runConfig = nil })
	runtime, err := buildEnvironmentJobRuntime("env-job", identified, mapping, &types.SetupEnvironments{Groups: []types.SetupEnvironment{{Name: "all"}}}, "1h")
	if err != nil {
		t.Fatalf("buildEnvironmentJobRuntime: %v", err)
	}
	if runtime.Parameters["QA_INFRA_REPO_URL"] != "https://example.test/infra" || runtime.Parameters["QA_INFRA_BRANCH"] != "release" {
		t.Fatalf("params = %#v", runtime.Parameters)
	}
}

func TestBuildEnvironmentJobRuntimeUsesTestsRepositoryInsteadOfProductRepository(t *testing.T) {
	parameters := map[string]any{}
	for _, name := range []string{"TERRAFORM_CONFIG", "ANSIBLE_VARIABLES", "CATTLE_TEST_CONFIG", "TEST_PACKAGE", "GOTEST_TESTCASE_VALIDATION", "VALIDATION_TEST_TAGS", "TEST_TIMEOUT", "INDIVIDUAL_JOB", "HOSTNAME_PREFIX", "TESTS_REPO_URL", "TESTS_BRANCH"} {
		parameters[name] = map[string]any{"default": "default"}
	}
	mapping := map[string]any{"job_mappings": map[string]any{"env-job": map[string]any{"qase_project": "RM", "parameters": parameters}}}
	identified := types.IdentifiedTests{PRNumber: 42, Repo: "rancher/rancher", BaseRef: "release-v2.15", Tests: []types.TestEntry{{QaseProjects: []string{"RM"}}}, TestsByProject: map[string][]int{"RM": {0}}}
	runConfig = &runconfig.Config{Tests: runconfig.RepositoryConfig{RepoURL: "https://github.com/rancher/tests", Branch: "main"}}
	t.Cleanup(func() { runConfig = nil })
	runtime, err := buildEnvironmentJobRuntime("env-job", identified, mapping, &types.SetupEnvironments{Groups: []types.SetupEnvironment{{Name: "all"}}}, "1h")
	if err != nil {
		t.Fatalf("buildEnvironmentJobRuntime: %v", err)
	}
	if runtime.Parameters["TESTS_REPO_URL"] != "https://github.com/rancher/tests" || runtime.Parameters["TESTS_BRANCH"] != "main" {
		t.Fatalf("tests params = %#v", runtime.Parameters)
	}
}
