package envplan

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/types"
	"gopkg.in/yaml.v3"
)

func TestUpstreamArtifactPaths(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig() // aws / default / rke2
	tfvars, clusterVars, rancherVars := UpstreamArtifactPaths(cfg)
	if tfvars != filepath.Join("tofu", "aws", "modules", "cluster_nodes", "terraform.tfvars") {
		t.Errorf("unexpected tfvars path: %s", tfvars)
	}
	if clusterVars != filepath.Join("ansible", "rke2", "default", "vars.yaml") {
		t.Errorf("unexpected cluster vars path: %s", clusterVars)
	}
	if rancherVars != filepath.Join("ansible", "rancher", "default-ha", "vars.yaml") {
		t.Errorf("unexpected rancher vars path: %s", rancherVars)
	}
}

func TestGenerateTerraformTfvars_RolesAndGrouping(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	up := types.UpstreamCluster{
		NodePools: []types.NodeRequirement{
			{Etcd: true, ControlPlane: true, Quantity: 3,
				Spec: &types.MachineSpec{InstanceType: "m5.xlarge", DiskGiB: 80}},
			{Worker: true, Quantity: 2},
		},
		TotalNodes: 5,
	}
	data, err := GenerateTerraformTfvars(up, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	// controlplane -> cp mapping.
	if !strings.Contains(out, `role = ["etcd", "cp"]`) {
		t.Errorf("expected etcd+cp role mapping, got:\n%s", out)
	}
	if !strings.Contains(out, `role = ["worker"]`) {
		t.Errorf("expected worker role, got:\n%s", out)
	}
	// counts.
	if !strings.Contains(out, "count = 3") || !strings.Contains(out, "count = 2") {
		t.Errorf("expected counts 3 and 2, got:\n%s", out)
	}
	// per-group instance type from spec.
	if !strings.Contains(out, `instance_type = "m5.xlarge"`) {
		t.Errorf("expected per-group instance_type, got:\n%s", out)
	}
	// volume size from spec disk (max).
	if !strings.Contains(out, "aws_volume_size = 80") {
		t.Errorf("expected volume size 80 from spec, got:\n%s", out)
	}
	// security group rendered as a list.
	if !strings.Contains(out, `aws_security_group = [`) {
		t.Errorf("expected aws_security_group list, got:\n%s", out)
	}
	// placeholders preserved.
	if !strings.Contains(out, "${AWS_REGION}") {
		t.Errorf("expected ${AWS_REGION} placeholder preserved, got:\n%s", out)
	}
	// airgap/proxy required vars present.
	if !strings.Contains(out, "airgap_setup = false") || !strings.Contains(out, "proxy_setup") {
		t.Errorf("expected airgap/proxy vars, got:\n%s", out)
	}
}

func TestGenerateTerraformTfvars_NonTopologyVarsCommentedOut(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	up := types.UpstreamCluster{
		NodePools: []types.NodeRequirement{
			{Etcd: true, ControlPlane: true, Worker: true, Quantity: 3},
		},
		TotalNodes: 3,
	}
	data, err := GenerateTerraformTfvars(up, cfg)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")

	// Every non-topology field from TfvarsTemplate must appear as a comment
	// (a "# <key> = " line), never as an active assignment.
	for key := range cfg.TfvarsTemplate {
		activeAssignment := key + " = "
		commentAssignment := "# " + key + " = "
		found := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, commentAssignment) {
				found = true
				continue
			}
			if strings.HasPrefix(trimmed, activeAssignment) {
				t.Errorf("expected %q to be commented out, found active assignment: %q", key, line)
			}
		}
		if !found {
			t.Errorf("expected commented-out assignment for %q, got:\n%s", key, string(data))
		}
	}

	// Topology fields must remain active (uncommented).
	for _, want := range []string{"aws_volume_size = ", "nodes = [", "airgap_setup = false", "proxy_setup  = false"} {
		found := false
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected active topology line starting with %q, got:\n%s", want, string(data))
		}
	}

	// Without resolved specs, instance_type stays commented (falls back to the
	// ${VAR} template line).
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "instance_type = ") {
			t.Errorf("expected instance_type to stay commented without a resolved spec, got active: %q", line)
		}
	}
}

func TestGenerateTerraformTfvars_TopLevelInstanceTypeActive(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	up := types.UpstreamCluster{
		NodePools: []types.NodeRequirement{
			{Etcd: true, ControlPlane: true, Worker: true, Quantity: 3,
				Spec: &types.MachineSpec{VCPUs: 4, MemoryGiB: 16, DiskGiB: 100, InstanceType: "t3a.xlarge"}},
		},
		TotalNodes: 3,
	}
	data, err := GenerateTerraformTfvars(up, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	lines := strings.Split(out, "\n")

	// Top-level instance_type must be an active assignment with the resolved
	// value, and must NOT also appear as a commented template line.
	activeFound, commentFound := false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == `instance_type = "t3a.xlarge"` {
			activeFound = true
		}
		if strings.HasPrefix(trimmed, "# instance_type = ") {
			commentFound = true
		}
	}
	if !activeFound {
		t.Errorf("expected active top-level instance_type = \"t3a.xlarge\", got:\n%s", out)
	}
	if commentFound {
		t.Errorf("did not expect a commented instance_type line when resolved, got:\n%s", out)
	}
}

// upstreamInstanceType should pick the largest pool by vCPU (then memory).
func TestUpstreamInstanceType_PicksLargest(t *testing.T) {
	up := types.UpstreamCluster{
		NodePools: []types.NodeRequirement{
			{Worker: true, Spec: &types.MachineSpec{VCPUs: 2, MemoryGiB: 8, InstanceType: "t3a.large"}},
			{Etcd: true, ControlPlane: true, Spec: &types.MachineSpec{VCPUs: 8, MemoryGiB: 32, InstanceType: "t3a.2xlarge"}},
			{Worker: true}, // no spec
		},
	}
	if got := upstreamInstanceType(up); got != "t3a.2xlarge" {
		t.Errorf("upstreamInstanceType = %q, want t3a.2xlarge", got)
	}

	// No resolved specs -> empty.
	empty := types.UpstreamCluster{NodePools: []types.NodeRequirement{{Worker: true}}}
	if got := upstreamInstanceType(empty); got != "" {
		t.Errorf("upstreamInstanceType (no specs) = %q, want empty", got)
	}
}

func TestGenerateTerraformTfvars_NonAWSUnsupported(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	cfg.Provider = types.ProviderHarvester
	if SupportsTfvars(cfg) {
		t.Fatal("harvester should not support tfvars")
	}
	if _, err := GenerateTerraformTfvars(types.UpstreamCluster{}, cfg); err == nil {
		t.Error("expected error for non-AWS provider")
	}
}

func TestTofuEnvFilePath(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig() // aws / default / rke2
	got := TofuEnvFilePath(cfg)
	want := filepath.Join("tofu", "aws", "modules", "cluster_nodes", "tofu.env.example")
	if got != want {
		t.Errorf("unexpected tofu.env.example path: got %s, want %s", got, want)
	}
}

func TestGenerateTofuEnvFile(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	up := types.UpstreamCluster{
		NodePools: []types.NodeRequirement{
			{Etcd: true, ControlPlane: true, Worker: true, Quantity: 3,
				Spec: &types.MachineSpec{VCPUs: 4, MemoryGiB: 16, InstanceType: "t3a.xlarge"}},
		},
		KubernetesDistro: types.DistroRKE2,
		CNI:              types.CNICalico,
		TotalNodes:       3,
	}
	data, err := GenerateTofuEnvFile(up, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	// The file is sourced directly by bash, so no raw ${VAR} shell-expansion
	// syntax should leak into any assignment's value -- it would silently
	// expand to empty instead of prompting the user to fill it in.
	if strings.Contains(out, "=${") {
		t.Errorf("tofu.env.example must not contain raw ${...} shell-expandable values, got:\n%s", out)
	}

	// Every non-topology tfvars field must appear both with and without the
	// TF_VAR_ prefix. Sensitive keys use a safe <VAR_NAME> placeholder derived
	// from the template's ${VAR_NAME} value; non-sensitive keys resolved by the
	// plan are pre-filled with concrete values. All RHS values are single-quoted
	// so the file is safe to `source`.
	for key, val := range cfg.TfvarsTemplate {
		if key == infraSecurityGroupField {
			// Special-cased: TF_VAR_ form is a single-quoted HCL/JSON list.
			if !strings.Contains(out, `TF_VAR_`+key+`='["<`) {
				t.Errorf("expected TF_VAR_%s to be a quoted HCL/JSON list literal, got:\n%s", key, out)
			}
			continue
		}
		want := envExamplePlaceholder(val)
		if key == infraInstanceTypeField {
			// Pre-filled from the resolved node spec instead of a placeholder.
			want = "t3a.xlarge"
		}
		prefixed := fmt.Sprintf("TF_VAR_%s=%s", key, shellSingleQuote(want))
		plain := fmt.Sprintf("%s=%s", key, shellSingleQuote(want))
		if !strings.Contains(out, prefixed) {
			t.Errorf("expected %q in tofu.env.example, got:\n%s", prefixed, out)
		}
		if !strings.Contains(out, plain) {
			t.Errorf("expected %q in tofu.env.example, got:\n%s", plain, out)
		}
	}

	// instance_type must be pre-filled (non-sensitive, resolved from the plan).
	if !strings.Contains(out, "TF_VAR_instance_type='t3a.xlarge'") {
		t.Errorf("expected instance_type pre-filled from resolved spec, got:\n%s", out)
	}

	// Non-sensitive ansible values (distro, CNI) must be pre-filled.
	if !strings.Contains(out, "KUBERNETES_DISTRO='rke2'") {
		t.Errorf("expected KUBERNETES_DISTRO pre-filled, got:\n%s", out)
	}
	if !strings.Contains(out, "CNI='calico'") {
		t.Errorf("expected CNI pre-filled, got:\n%s", out)
	}

	// Identified provider/env from the plan must be surfaced.
	if !strings.Contains(out, "PROVIDER='aws'") {
		t.Errorf("expected PROVIDER pre-filled from plan, got:\n%s", out)
	}
	if !strings.Contains(out, "ENV='default'") {
		t.Errorf("expected ENV='default' pre-filled from plan, got:\n%s", out)
	}

	// Sensitive ansible/tofu values should remain <PLACEHOLDER>.
	if !strings.Contains(out, "TF_VAR_aws_access_key='<AWS_ACCESS_KEY>'") {
		t.Errorf("expected sensitive aws_access_key to stay a placeholder, got:\n%s", out)
	}

	// Ansible-stage version/password vars should be present.
	for _, key := range []string{"RKE2_VERSION", "RANCHER_VERSION", "RANCHER_IMAGE_TAG", "CERT_MANAGER_VERSION", "RANCHER_FQDN", "RANCHER_BOOTSTRAP_PASSWORD", "RANCHER_PASSWORD"} {
		if !strings.Contains(out, key+"=") {
			t.Errorf("expected %q in tofu.env.example, got:\n%s", key, out)
		}
	}
}

// When the tfvars template carries concrete literal values (not ${VAR}
// placeholders), the env file must reflect them verbatim rather than turning
// them into <PLACEHOLDER>s.
func TestGenerateTofuEnvFile_ConcreteLiteralsPreFilled(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	cfg.TfvarsTemplate["aws_region"] = "us-east-2"
	cfg.TfvarsTemplate["aws_ssh_user"] = "ec2-user"
	cfg.TfvarsTemplate["aws_volume_type"] = "gp3"
	cfg.TfvarsTemplate["aws_security_group"] = "sg-0abc123,sg-0def456"

	up := types.UpstreamCluster{
		NodePools: []types.NodeRequirement{
			{Etcd: true, ControlPlane: true, Worker: true, Quantity: 3,
				Spec: &types.MachineSpec{VCPUs: 4, MemoryGiB: 16, DiskGiB: 100, InstanceType: "t3a.xlarge"}},
		},
		KubernetesDistro: types.DistroRKE2,
		CNI:              types.CNICalico,
		Provider:         types.ProviderAWS,
		Env:              "default",
		TotalNodes:       3,
	}
	data, err := GenerateTofuEnvFile(up, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	for _, want := range []string{
		"TF_VAR_aws_region='us-east-2'",
		"aws_region='us-east-2'",
		"TF_VAR_aws_ssh_user='ec2-user'",
		"TF_VAR_aws_volume_type='gp3'",
		// concrete security-group list rendered as an HCL list literal.
		`TF_VAR_aws_security_group='["sg-0abc123", "sg-0def456"]'`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q pre-filled in tofu.env.example, got:\n%s", want, out)
		}
	}
}

func TestResolveMakefileEnv(t *testing.T) {
	cases := map[string]string{
		"":           "default",
		"default":    "default",
		"Default":    "default",
		"airgap":     "airgap",
		"Airgap":     "airgap",
		" airgap ":   "airgap",
		"proxy":      "proxy",
		"custom-env": "custom-env",
	}
	for in, want := range cases {
		if got := resolveMakefileEnv(in); got != want {
			t.Errorf("resolveMakefileEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

// GenerateTofuEnvFile must set ENV='airgap' for an airgapped plan.
func TestGenerateTofuEnvFile_AirgapEnv(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	up := types.UpstreamCluster{
		NodePools: []types.NodeRequirement{
			{Etcd: true, ControlPlane: true, Worker: true, Quantity: 3},
		},
		Provider:   types.ProviderAWS,
		Env:        "airgap",
		TotalNodes: 3,
	}
	data, err := GenerateTofuEnvFile(up, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "ENV='airgap'") {
		t.Errorf("expected ENV='airgap' for an airgapped plan, got:\n%s", out)
	}
}

// GenerateTofuEnvFile must default ENV to 'default' when unset.
func TestGenerateTofuEnvFile_DefaultEnv(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	cfg.Env = ""
	up := types.UpstreamCluster{
		NodePools: []types.NodeRequirement{
			{Etcd: true, ControlPlane: true, Worker: true, Quantity: 3},
		},
		Provider:   types.ProviderAWS,
		TotalNodes: 3,
	}
	data, err := GenerateTofuEnvFile(up, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "ENV='default'") {
		t.Errorf("expected ENV='default' when unset, got:\n%s", out)
	}
}

// GenerateTofuEnvFile must pass through any other qa-infra-automation
// environment (e.g. "proxy") unchanged -- it already IS the PROVIDER-specific
// tofu module foldername under the qa-infra-automation Makefile scheme.
func TestGenerateTofuEnvFile_CustomEnvPassthrough(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	up := types.UpstreamCluster{
		NodePools: []types.NodeRequirement{
			{Etcd: true, ControlPlane: true, Worker: true, Quantity: 3},
		},
		Provider:   types.ProviderAWS,
		Env:        "proxy",
		TotalNodes: 3,
	}
	data, err := GenerateTofuEnvFile(up, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "ENV='proxy'") {
		t.Errorf("expected ENV='proxy' passthrough, got:\n%s", out)
	}
}

func TestEnvExamplePlaceholder(t *testing.T) {
	cases := map[string]string{
		"${AWS_ACCESS_KEY}": "<AWS_ACCESS_KEY>",
		"literal-value":     "literal-value",
		"":                  "",
	}
	for in, want := range cases {
		if got := envExamplePlaceholder(in); got != want {
			t.Errorf("envExamplePlaceholder(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGenerateClusterVarsYAML_RKE2(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig() // rke2
	data, err := GenerateClusterVarsYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid YAML: %v\n%s", err, data)
	}
	if parsed["kubernetes_version"] != "${RKE2_VERSION}" {
		t.Errorf("expected rke2 version placeholder, got %v", parsed["kubernetes_version"])
	}
	if parsed["cni"] != types.CNICalico {
		t.Errorf("expected cni calico, got %v", parsed["cni"])
	}
	if _, ok := parsed["channel"]; ok {
		t.Error("rke2 vars should not have a k3s 'channel' key")
	}
}

func TestGenerateClusterVarsYAML_K3s(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	cfg.KubernetesDistro = types.DistroK3S
	cfg.KubernetesVersion = "${K3S_VERSION}"
	data, err := GenerateClusterVarsYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["channel"]; !ok {
		t.Errorf("k3s vars should include 'channel', got %v", parsed)
	}
	if _, ok := parsed["worker_flags"]; !ok {
		t.Errorf("k3s vars should include 'worker_flags', got %v", parsed)
	}
}

func TestGenerateRancherVarsYAML(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	data, err := GenerateRancherVarsYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"rancher_version", "rancher_image_tag", "cert_manager_version", "fqdn", "bootstrap_password", "password"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("rancher vars missing %q: %v", key, parsed)
		}
	}
	// rancher_chart_repo/_url must be entirely absent (not present-but-empty)
	// when unset, since the playbook's Jinja `default()` filter only
	// substitutes for an undefined variable, not an empty string.
	for _, key := range []string{"rancher_chart_repo", "rancher_chart_repo_url"} {
		if _, ok := parsed[key]; ok {
			t.Errorf("rancher vars should omit %q when unset, got %v", key, parsed[key])
		}
	}
}

func TestGenerateRancherVarsYAML_ChartRepoOverride(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	cfg.RancherChartRepo = "rancher-alpha"
	cfg.RancherChartRepoURL = "https://releases.rancher.com/server-charts/alpha"

	data, err := GenerateRancherVarsYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["rancher_chart_repo"] != "rancher-alpha" {
		t.Errorf("rancher_chart_repo = %v, want rancher-alpha", parsed["rancher_chart_repo"])
	}
	if parsed["rancher_chart_repo_url"] != "https://releases.rancher.com/server-charts/alpha" {
		t.Errorf("rancher_chart_repo_url = %v, want the alpha channel URL", parsed["rancher_chart_repo_url"])
	}
}
