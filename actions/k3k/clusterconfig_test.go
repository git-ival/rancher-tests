package k3k

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes a config file with the boilerplate every case needs.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	t.Setenv("TEST_RANCHER_TOKEN", "token-id:secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	header := `batch: k3k-ci
rancher:
  host: rancher.example.com
  tokenEnv: TEST_RANCHER_TOKEN
targets:
  - name: downstream-0-0
    kubeconfig: /downstream-0-0.yaml
    kubeconfigServer: 192.0.2.10
`
	if err := os.WriteFile(path, []byte(header+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigParsesClusterSpec(t *testing.T) {
	path := writeConfig(t, `count: 1
clusterSpec:
  mode: virtual
  servers: 3
  agents: 3
  persistence:
    type: dynamic
    storageClassName: local-path
    storageRequestSize: 30Gi
  serverLimit:
    cpu: "1"
    memory: 2Gi
  serverEnvs:
    - name: HTTP_PROXY
      value: http://abc.xyz
  serverAffinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
          - matchExpressions:
              - key: disktype
                operator: In
                values: ["ssd"]
`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	spec := config.ClusterSpec
	if spec.Mode != ModeVirtual || *spec.Servers != 3 || *spec.Agents != 3 {
		t.Fatalf("topology not parsed: %#v", spec)
	}
	if spec.Persistence.StorageRequestSize.String() != "30Gi" {
		t.Fatalf("storageRequestSize not parsed: %v", spec.Persistence.StorageRequestSize)
	}
	if spec.ServerLimit.Memory().String() != "2Gi" {
		t.Fatalf("serverLimit not parsed: %v", spec.ServerLimit)
	}
	if spec.ServerEnvs[0].Name != "HTTP_PROXY" {
		t.Fatalf("serverEnvs not parsed: %v", spec.ServerEnvs)
	}
	if spec.ServerAffinity == nil || spec.ServerAffinity.NodeAffinity == nil {
		t.Fatalf("serverAffinity not parsed: %#v", spec.ServerAffinity)
	}
}

// TestDeprecatedAliasesFold keeps existing configuration files working after clusterSpec was
// introduced.
func TestDeprecatedAliasesFold(t *testing.T) {
	path := writeConfig(t, `count: 1
mode: virtual
servers: 2
agents: 2
persistenceType: dynamic
`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	spec := config.ClusterSpec
	if spec.Mode != ModeVirtual || *spec.Servers != 2 || *spec.Agents != 2 {
		t.Fatalf("aliases were not folded into clusterSpec: %#v", spec)
	}
	if spec.Persistence == nil || spec.Persistence.Type != PersistenceDynamic {
		t.Fatalf("persistenceType was not folded: %#v", spec.Persistence)
	}
	if len(config.Warnings) == 0 {
		t.Fatal("expected deprecation warnings")
	}
}

// TestDeprecatedAliasConflict prevents two sources of truth from disagreeing silently.
func TestDeprecatedAliasConflict(t *testing.T) {
	path := writeConfig(t, `count: 1
mode: shared
clusterSpec:
  mode: virtual
`)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected a conflict between mode and clusterSpec.mode")
	}
	if !strings.Contains(err.Error(), "clusterSpec.mode") {
		t.Fatalf("error should name the conflicting field: %v", err)
	}
}

// TestDefaultsStillApplyWithoutClusterSpec pins the pre-existing behaviour.
func TestDefaultsStillApplyWithoutClusterSpec(t *testing.T) {
	path := writeConfig(t, "count: 1\n")

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.ClusterSpec.Mode != ModeShared || *config.ClusterSpec.Servers != 1 {
		t.Fatalf("defaults were not applied: %#v", config.ClusterSpec)
	}
	if config.ClusterSpec.Persistence.Type != PersistenceEphemeral {
		t.Fatalf("persistence default was not applied: %#v", config.ClusterSpec.Persistence)
	}
	if config.LBAddressTimeout == 0 {
		t.Fatal("lbAddressTimeout default was not applied")
	}
}

func TestClusterSpecValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "expose modes are mutually exclusive",
			body: "count: 1\nclusterSpec:\n  tlsSANs: [a.example.com]\n  expose:\n    ingress: {}\n    nodePort: {}\n",
			want: "mutually exclusive",
		},
		{
			name: "ingress requires a SAN to use as its host",
			body: "count: 1\nclusterSpec:\n  expose:\n    ingress:\n      ingressClassName: nginx\n",
			want: "tlsSANs",
		},
		{
			name: "serverResources would silently discard serverLimit",
			body: "count: 1\nclusterSpec:\n  serverLimit:\n    cpu: \"1\"\n  serverResources:\n    limits:\n      cpu: \"1\"\n",
			want: "serverResources overrides serverLimit",
		},
		{
			name: "workerResources would silently discard workerLimit",
			body: "count: 1\nclusterSpec:\n  workerLimit:\n    cpu: \"1\"\n  workerResources:\n    limits:\n      cpu: \"1\"\n",
			want: "workerResources overrides workerLimit",
		},
		{
			name: "agents require virtual mode",
			body: "count: 1\nclusterSpec:\n  mode: shared\n  agents: 2\n",
			want: "virtual mode",
		},
		{
			name: "mode must be a known value",
			body: "count: 1\nclusterSpec:\n  mode: bogus\n",
			want: "clusterSpec.mode must be",
		},
		{
			name: "persistence type must be a known value",
			body: "count: 1\nclusterSpec:\n  persistence:\n    type: bogus\n",
			want: "persistence.type must be",
		},
		{
			name: "unsupported fields are rejected",
			body: "count: 1\nclusterSpec:\n  sync:\n    services:\n      enabled: true\n",
			want: "parse clusterSpec",
		},
		{
			name: "a batch of ingress clusters needs per-cluster hostnames",
			body: "count: 3\nclusterSpec:\n  tlsSANs: [one.example.com]\n  expose:\n    ingress:\n      ingressClassName: nginx\n",
			want: "template expression",
		},
		{
			name: "a batch with fixed node ports would collide",
			body: "count: 3\nclusterSpec:\n  expose:\n    nodePort:\n      serverPort: 30443\n",
			want: "fixed ports",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, testCase.body))
			if err == nil {
				t.Fatalf("expected an error containing %q", testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not contain %q", err, testCase.want)
			}
		})
	}
}

// TestTemplatedBatchIsAccepted is the counterpart to the uniqueness rejections: the same
// configuration is valid once the value varies per cluster.
func TestTemplatedBatchIsAccepted(t *testing.T) {
	path := writeConfig(t, `count: 3
clusterSpec:
  tlsSANs: ["{{ .Name }}.k3k.example.com"]
  expose:
    ingress:
      ingressClassName: nginx
`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.ClusterSpec.TLSSANs[0] != "k3k-ci-001.k3k.example.com" {
		t.Fatalf("sample render used the wrong context: %v", config.ClusterSpec.TLSSANs)
	}
}

// TestUnquotedTemplateInNumericField is the reason the clusterSpec block is split lexically
// rather than parsed as YAML first: "{{ ... }}" at the start of a value is a flow mapping to a
// YAML parser, so the block cannot be parsed until it has been rendered.
func TestUnquotedTemplateInNumericField(t *testing.T) {
	path := writeConfig(t, `count: 3
clusterSpec:
  expose:
    nodePort:
      serverPort: {{ add 30400 .Index }}
      etcdPort: {{ add 31400 .Index }}
`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := *config.ClusterSpec.Expose.NodePort.ServerPort; got != 30401 {
		t.Fatalf("serverPort = %d, want 30401 for the sample context", got)
	}

	item := &ClusterState{Name: "k3k-ci-003", Namespace: "k3k-k3k-ci-003"}
	spec, err := config.SpecFor(item, config.Targets[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := *spec.Expose.NodePort.ServerPort; got != 30403 {
		t.Fatalf("serverPort = %d, want 30403 for cluster 003", got)
	}
	if got := *spec.Expose.NodePort.ETCDPort; got != 31403 {
		t.Fatalf("etcdPort = %d, want 31403 for cluster 003", got)
	}
}

// TestClusterSpecBlockSplit pins the lexical boundaries of the block.
func TestClusterSpecBlockSplit(t *testing.T) {
	cases := []struct {
		name      string
		source    string
		wantBlock string
		wantOuter []string
	}{
		{
			name:      "block ends at the next top-level key",
			source:    "batch: a\nclusterSpec:\n  mode: shared\n\n  servers: 1\ncount: 2\n",
			wantBlock: "  mode: shared\n\n  servers: 1",
			wantOuter: []string{"batch: a", "count: 2"},
		},
		{
			name:      "no clusterSpec leaves the document untouched",
			source:    "batch: a\ncount: 2\n",
			wantBlock: "",
			wantOuter: []string{"batch: a", "count: 2"},
		},
		{
			name:      "inline flow mapping is preserved",
			source:    "batch: a\nclusterSpec: {mode: shared}\ncount: 2\n",
			wantBlock: "{mode: shared}",
			wantOuter: []string{"batch: a", "count: 2"},
		},
		{
			name:      "a nested clusterSpec key is not mistaken for the block",
			source:    "batch: a\nrancher:\n  clusterSpec: no\ncount: 2\n",
			wantBlock: "",
			wantOuter: []string{"batch: a", "clusterSpec: no", "count: 2"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			outer, block := splitClusterSpecBlock([]byte(testCase.source))
			if string(block) != testCase.wantBlock {
				t.Fatalf("block = %q, want %q", block, testCase.wantBlock)
			}
			for _, want := range testCase.wantOuter {
				if !strings.Contains(string(outer), want) {
					t.Fatalf("outer document lost %q:\n%s", want, outer)
				}
			}
			if strings.Count(string(outer), "\n") != strings.Count(testCase.source, "\n") {
				t.Fatalf("line count changed, which would shift YAML error positions:\n%s", outer)
			}
		})
	}
}

// TestVersionManagementDefaultsOff pins the default that keeps imported k3k clusters from
// getting stuck.
//
// Rancher classifies a k3k virtual cluster as an imported K3s cluster. With version management
// on it deploys system-upgrade-controller into the cluster and creates upgrade Plans for its
// nodes; a virtual cluster has no machines to upgrade, so the Plan stays in Applying and the
// cluster never reaches active.
func TestVersionManagementDefaultsOff(t *testing.T) {
	config, err := LoadConfig(writeConfig(t, "count: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Rancher.VersionManagement != "false" {
		t.Fatalf("versionManagement = %q, want false", config.Rancher.VersionManagement)
	}
}

func TestVersionManagementIsValidated(t *testing.T) {
	write := func(t *testing.T, value string) string {
		t.Helper()
		t.Setenv("TEST_RANCHER_TOKEN", "token-id:secret")
		path := filepath.Join(t.TempDir(), "config.yaml")
		body := `batch: k3k-ci
count: 1
rancher:
  host: rancher.example.com
  tokenEnv: TEST_RANCHER_TOKEN
  versionManagement: "` + value + `"
targets:
  - name: downstream-0-0
    kubeconfig: /downstream-0-0.yaml
    kubeconfigServer: 192.0.2.10
`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	for _, value := range []string{"true", "false", "system-default"} {
		config, err := LoadConfig(write(t, value))
		if err != nil {
			t.Fatalf("%q should be accepted: %v", value, err)
		}
		if config.Rancher.VersionManagement != value {
			t.Fatalf("versionManagement = %q, want %q", config.Rancher.VersionManagement, value)
		}
	}

	if _, err := LoadConfig(write(t, "maybe")); err == nil {
		t.Fatal("an invalid versionManagement value should be rejected")
	}
}

// TestPodLevelResourcesWarning surfaces the host requirement rather than failing.
func TestPodLevelResourcesWarning(t *testing.T) {
	path := writeConfig(t, "count: 1\nclusterSpec:\n  serverResources:\n    limits:\n      cpu: \"1\"\n")

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, warning := range config.Warnings {
		if strings.Contains(warning, "PodLevelResources") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a PodLevelResources warning, got %v", config.Warnings)
	}
}
