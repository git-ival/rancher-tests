package envplan

import (
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
}
