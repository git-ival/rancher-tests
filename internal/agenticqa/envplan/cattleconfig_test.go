package envplan

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/types"
	"gopkg.in/yaml.v3"
)

func testTemplate() envconfig.CattleConfigTemplate {
	return envconfig.Generate().CattleConfig
}

func TestGenerateCattleConfig_Valid(t *testing.T) {
	g := types.EnvironmentGroup{
		Name: "all",
		Cluster: types.ClusterRequirement{
			NodePools: []types.NodeRequirement{
				{Etcd: true, Quantity: 3},
				{ControlPlane: true, Quantity: 2},
				{Worker: true, Quantity: 3},
			},
			KubernetesDistro:  "rke2",
			KubernetesVersion: "v1.28.5+rke2r1",
			CNI:               "calico",
			Provider:          "aws",
			NodeProvider:      "ec2",
			Downstream:        true,
			TotalNodes:        8,
			Networking:        &types.NetworkingRequirement{LocalClusterAuthEndpoint: true},
		},
	}

	data, err := GenerateCattleConfig(g, testTemplate(), nil)
	if err != nil {
		t.Fatalf("GenerateCattleConfig error: %v", err)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("generated cattle-config is not valid YAML: %v\n%s", err, data)
	}

	// --- full-config sections must all be present ---
	for _, key := range []string{
		"rancher", "registryInput", "awsCredentials",
		"provisioningInput", "clusterConfig", "awsMachineConfigs",
		"awsEC2Configs", "sshPath",
	} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("missing top-level section %q\n%s", key, data)
		}
	}

	rancher := parsed["rancher"].(map[string]any)
	if rancher["host"] != "${RANCHER_HOST}" {
		t.Errorf("expected rancher.host placeholder, got %v", rancher["host"])
	}

	provIn := parsed["provisioningInput"].(map[string]any)
	if _, ok := provIn["machinePools"]; !ok {
		t.Error("provisioningInput missing machinePools")
	}
	if v, _ := provIn["rke2KubernetesVersion"].([]any); len(v) != 1 || v[0] != "v1.28.5+rke2r1" {
		t.Errorf("expected pinned rke2KubernetesVersion, got %v", provIn["rke2KubernetesVersion"])
	}

	clusterCfg := parsed["clusterConfig"].(map[string]any)
	if clusterCfg["provider"] != "aws" {
		t.Errorf("expected provider aws, got %v", clusterCfg["provider"])
	}
	if _, ok := clusterCfg["registries"]; !ok {
		t.Error("expected clusterConfig.registries from template")
	}
	net := clusterCfg["networking"].(map[string]any)
	ace, ok := net["localClusterAuthEndpoint"].(map[string]any)
	if !ok || ace["enabled"] != true {
		t.Errorf("expected localClusterAuthEndpoint.enabled=true, got %v", net["localClusterAuthEndpoint"])
	}

	// --- aws machine config: one entry per pool, roles injected per pool ---
	awsMC := parsed["awsMachineConfigs"].(map[string]any)
	if awsMC["region"] != "${AWS_REGION}" {
		t.Errorf("expected awsMachineConfigs.region placeholder, got %v", awsMC["region"])
	}
	mcList := awsMC["awsMachineConfig"].([]any)
	// 3 pools (etcd, controlplane, worker) → 3 entries.
	if len(mcList) != 3 {
		t.Fatalf("expected 3 machine-config entries (one per pool), got %d", len(mcList))
	}
	// Each entry must have exactly one role.
	for i, raw := range mcList {
		entry := raw.(map[string]any)
		roles, ok := entry["roles"].([]any)
		if !ok || len(roles) != 1 {
			t.Errorf("entry %d: expected 1 role (per-pool), got %v", i, entry["roles"])
		}
	}
	// Worker entry (last pool) must have securityGroup as a list.
	workerEntry := mcList[2].(map[string]any)
	if sg, ok := workerEntry["securityGroup"].([]any); !ok || len(sg) != 1 {
		t.Errorf("expected worker securityGroup rendered as list, got %v", workerEntry["securityGroup"])
	}

	// ssh path nests under sshPath.sshPath
	ssh := parsed["sshPath"].(map[string]any)
	if ssh["sshPath"] != "${SSH_PRIVATE_KEY_PATH}" {
		t.Errorf("expected sshPath.sshPath placeholder, got %v", ssh["sshPath"])
	}

	if !strings.Contains(string(data), "Total downstream nodes: 8") {
		t.Error("expected node-count comment in header")
	}
	if !strings.Contains(string(data), "envsubst") {
		t.Error("expected envsubst guidance in header")
	}
}

func TestGenerateCattleConfig_K3sVersionKey(t *testing.T) {
	g := types.EnvironmentGroup{
		Name: "k3s",
		Cluster: types.ClusterRequirement{
			NodePools:        []types.NodeRequirement{{Etcd: true, ControlPlane: true, Worker: true, Quantity: 1}},
			KubernetesDistro: "k3s",
			Provider:         "aws",
			Downstream:       true,
			TotalNodes:       1,
		},
	}
	data, err := GenerateCattleConfig(g, testTemplate(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	provIn := parsed["provisioningInput"].(map[string]any)
	// No explicit version pinned: should fall back to the k3s env placeholder.
	v, ok := provIn["k3sKubernetesVersion"].([]any)
	if !ok || v[0] != "${K3S_VERSION}" {
		t.Errorf("expected k3sKubernetesVersion env placeholder, got %v", provIn["k3sKubernetesVersion"])
	}
	if _, ok := provIn["rke2KubernetesVersion"]; ok {
		t.Error("did not expect rke2KubernetesVersion key for k3s distro")
	}
}

// TestGenerateCattleConfig_WithRecommendedSpecs verifies that, when node pools
// carry a MachineSpec, the generator selects an AWS instanceType from the
// catalog and overrides rootSize / volumeSize while leaving secrets as
// placeholders.
func TestGenerateCattleConfig_WithRecommendedSpecs(t *testing.T) {
	g := types.EnvironmentGroup{
		Name: "all",
		Cluster: types.ClusterRequirement{
			NodePools: []types.NodeRequirement{
				{Etcd: true, Quantity: 3, Spec: &types.MachineSpec{VCPUs: 2, MemoryGiB: 8, DiskGiB: 40}},
				{Worker: true, Quantity: 3, Spec: &types.MachineSpec{VCPUs: 4, MemoryGiB: 16, DiskGiB: 100}},
			},
			Provider:     "aws",
			NodeProvider: "ec2",
			Downstream:   true,
			TotalNodes:   6,
		},
	}
	data, err := GenerateCattleConfig(g, testTemplate(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid YAML: %v\n%s", err, data)
	}

	awsMC := parsed["awsMachineConfigs"].(map[string]any)
	mcList := awsMC["awsMachineConfig"].([]any)
	// 2 pools → 2 entries; each pool gets its own instance type.
	if len(mcList) != 2 {
		t.Fatalf("expected 2 machine-config entries (one per pool), got %d", len(mcList))
	}
	etcdEntry := mcList[0].(map[string]any)
	workerEntry := mcList[1].(map[string]any)

	// etcd pool: 2 vCPU / 8 GiB → smallest fit in catalog.
	etcdIT, _ := testTemplate().SelectInstanceType("aws", 2, 8)
	if etcdEntry["instanceType"] != etcdIT.Name {
		t.Errorf("etcd pool instanceType: want %q, got %v", etcdIT.Name, etcdEntry["instanceType"])
	}
	if etcdEntry["rootSize"] != "40" {
		t.Errorf("etcd pool rootSize: want 40, got %v", etcdEntry["rootSize"])
	}

	// worker pool: 4 vCPU / 16 GiB / 100 GiB disk → smallest fit in catalog.
	workerIT, _ := testTemplate().SelectInstanceType("aws", 4, 16)
	if workerEntry["instanceType"] != workerIT.Name {
		t.Errorf("worker pool instanceType: want %q, got %v", workerIT.Name, workerEntry["instanceType"])
	}
	if workerEntry["rootSize"] != "100" {
		t.Errorf("worker pool rootSize: want 100, got %v", workerEntry["rootSize"])
	}
	// Secrets stay as placeholders on every entry.
	if workerEntry["ami"] != "${AWS_AMI}" {
		t.Errorf("expected ami placeholder preserved, got %v", workerEntry["ami"])
	}

	// EC2 config uses the worker pool's spec (the pool that runs workloads).
	ec2 := parsed["awsEC2Configs"].(map[string]any)
	ec2entry := ec2["awsEC2Config"].([]any)[0].(map[string]any)
	if ec2entry["volumeSize"] != 100 {
		t.Errorf("expected ec2 volumeSize 100 (int) from worker pool, got %v (%T)", ec2entry["volumeSize"], ec2entry["volumeSize"])
	}
	if ec2entry["instanceType"] != workerIT.Name {
		t.Errorf("expected ec2 instanceType %q from worker pool, got %v", workerIT.Name, ec2entry["instanceType"])
	}
}

// TestGenerateCattleConfig_NoSpecsKeepsPlaceholders verifies that without specs
// the machine fields remain template placeholders (backward compatible).
func TestGenerateCattleConfig_NoSpecsKeepsPlaceholders(t *testing.T) {
	g := types.EnvironmentGroup{
		Name: "all",
		Cluster: types.ClusterRequirement{
			NodePools:  []types.NodeRequirement{{Etcd: true, ControlPlane: true, Worker: true, Quantity: 1}},
			Provider:   "aws",
			Downstream: true,
			TotalNodes: 1,
		},
	}
	data, err := GenerateCattleConfig(g, testTemplate(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	_ = yaml.Unmarshal(data, &parsed)
	entry := parsed["awsMachineConfigs"].(map[string]any)["awsMachineConfig"].([]any)[0].(map[string]any)
	if entry["instanceType"] != "${AWS_INSTANCE_TYPE}" {
		t.Errorf("expected instanceType placeholder without specs, got %v", entry["instanceType"])
	}
	if entry["rootSize"] != "${AWS_ROOT_SIZE}" {
		t.Errorf("expected rootSize placeholder without specs, got %v", entry["rootSize"])
	}
}

// TestGenerateCattleConfig_EnvsubstRoundTrip verifies the generated file is a
// valid template: after substituting env vars (as the pipeline's envsubst step
// would), the result is valid YAML with the substituted values and no leftover
// ${...} placeholders, and integer-typed fields parse as ints.
func TestGenerateCattleConfig_EnvsubstRoundTrip(t *testing.T) {
	g := types.EnvironmentGroup{
		Name: "all",
		Cluster: types.ClusterRequirement{
			NodePools:        []types.NodeRequirement{{Etcd: true, ControlPlane: true, Worker: true, Quantity: 1}},
			KubernetesDistro: "rke2",
			Provider:         "aws",
			NodeProvider:     "ec2",
			Downstream:       true,
			TotalNodes:       1,
		},
	}
	data, err := GenerateCattleConfig(g, testTemplate(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate envsubst with a representative environment.
	env := map[string]string{
		"RANCHER_HOST":             "rancher.example.com",
		"RANCHER_ADMIN_TOKEN":      "token-abc",
		"RANCHER_ADMIN_PASSWORD":   "p4ss",
		"CLUSTER_NAME":             "local",
		"QUAY_REGISTRY_NAME":       "quay.io",
		"QUAY_REGISTRY_USERNAME":   "quser",
		"QUAY_REGISTRY_PASSWORD":   "qpass",
		"AWS_ACCESS_KEY":           "AKIA",
		"AWS_SECRET_KEY":           "secret",
		"AWS_REGION":               "us-east-2",
		"AWS_AMI":                  "ami-123",
		"AWS_INSTANCE_TYPE":        "t3.xlarge",
		"AWS_USER":                 "ubuntu",
		"AWS_VPC_ID":               "vpc-1",
		"AWS_VOLUME_TYPE":          "gp3",
		"AWS_ZONE_LETTER":          "a",
		"AWS_ROOT_SIZE":            "100",
		"AWS_SECURITY_GROUP_NAMES": "sg-name",
		"AWS_SECURITY_GROUPS":      "sg-123",
		"AWS_SUBNET_ID":            "subnet-1",
		"AWS_IAM_PROFILE":          "profile",
		"SSH_PRIVATE_KEY_NAME":     "key",
		"SSH_PRIVATE_KEY_PATH":     "/home/u/.ssh",
		"QA_PRIVATE_REGISTRY_NAME": "registry.local",
		"DOCKERHUB_USERNAME":       "dhuser",
		"DOCKERHUB_PASSWORD":       "dhpass",
		"RKE2_VERSION":             "v1.28.5+rke2r1",
	}
	expanded := os.Expand(string(data), func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		return "MISSING_" + k
	})

	if strings.Contains(stripComments(expanded), "MISSING_") {
		t.Errorf("placeholder referenced an env var not provided by the pipeline:\n%s", expanded)
	}
	if leftover := regexp.MustCompile(`\$\{[A-Z0-9_]+\}`).FindString(stripComments(expanded)); leftover != "" {
		t.Errorf("leftover placeholder after expansion: %s", leftover)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(expanded), &parsed); err != nil {
		t.Fatalf("expanded cattle-config is not valid YAML: %v\n%s", err, expanded)
	}

	// volumeSize is an int-typed destination field; verify it parsed as int.
	ec2 := parsed["awsEC2Configs"].(map[string]any)
	entry := ec2["awsEC2Config"].([]any)[0].(map[string]any)
	if _, ok := entry["volumeSize"].(int); !ok {
		t.Errorf("expected volumeSize to parse as int, got %T (%v)", entry["volumeSize"], entry["volumeSize"])
	}
	rancher := parsed["rancher"].(map[string]any)
	if rancher["host"] != "rancher.example.com" {
		t.Errorf("expected expanded rancher.host, got %v", rancher["host"])
	}
}

// stripComments removes the leading "# ..." header lines so placeholder
// assertions only look at the YAML body.
func stripComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
