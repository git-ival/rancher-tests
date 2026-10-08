package k3k

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingRunner captures the commands the CLI issues and serves canned output.
type recordingRunner struct {
	calls []string
	// outputs maps a substring of the joined command to the stdout it should produce.
	outputs map[string]string
	// stdin records the payload piped to each command, keyed the same way.
	stdin map[string][]byte
}

func newRecordingRunner() *recordingRunner {
	return &recordingRunner{outputs: map[string]string{}, stdin: map[string][]byte{}}
}

func (r *recordingRunner) Run(_ context.Context, _, name string, args []string, stdin []byte, _ string) error {
	joined := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, joined)
	if stdin != nil {
		r.stdin[joined] = stdin
	}
	return nil
}

func (r *recordingRunner) Output(_ context.Context, _, name string, args []string, _ []byte, _ string) ([]byte, error) {
	joined := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, joined)
	for key, value := range r.outputs {
		if strings.Contains(joined, key) {
			return []byte(value), nil
		}
	}
	return []byte(`{"items":[]}`), nil
}

func (r *recordingRunner) find(substring string) string {
	for _, call := range r.calls {
		if strings.Contains(call, substring) {
			return call
		}
	}
	return ""
}

func createFixture(t *testing.T, spec string) (*Config, *ClusterState, Target) {
	t.Helper()
	config := testConfig(t, spec)
	config.CreateTimeout = 10 * time.Minute
	config.LBAddressTimeout = 50 * time.Millisecond

	dir := t.TempDir()
	item := testItem()
	item.ArtifactDir = dir
	item.Kubeconfig = filepath.Join(dir, "k3k-k3k-ci-001-k3k-ci-001-kubeconfig.yaml")

	target := testTarget()
	target.Kubeconfig = "/downstream-0-0.yaml"
	return config, item, target
}

// TestCreateAppliesManifestThenGeneratesKubeconfig pins the new create sequence.
//
// The Cluster is applied as a manifest because k3kcli exposes no flags for expose,
// nodeSelector, resource limits, or affinity, and overwrites spec.expose. k3kcli is still
// used for the kubeconfig, which requires the cluster CA.
func TestCreateAppliesManifestThenGeneratesKubeconfig(t *testing.T) {
	config, item, target := createFixture(t, "")
	runner := newRecordingRunner()
	cli := CLI{K3KCLI: "k3kcli", Kubectl: "kubectl", Runner: runner}

	if err := cli.Create(context.Background(), item, target, config); err != nil {
		t.Fatal(err)
	}

	if len(runner.calls) != 3 {
		t.Fatalf("expected apply, wait, and kubeconfig; got %v", runner.calls)
	}
	if !strings.HasPrefix(runner.calls[0], "kubectl") || !strings.Contains(runner.calls[0], "apply -f -") {
		t.Fatalf("first call should apply the manifest: %q", runner.calls[0])
	}
	if !strings.Contains(runner.calls[1], "wait --for=condition=Ready") ||
		!strings.Contains(runner.calls[1], "cluster.k3k.io/k3k-ci-001") {
		t.Fatalf("second call should wait for readiness: %q", runner.calls[1])
	}
	if !strings.HasPrefix(runner.calls[2], "k3kcli") || !strings.Contains(runner.calls[2], "kubeconfig generate") {
		t.Fatalf("third call should generate the kubeconfig: %q", runner.calls[2])
	}

	// The kubeconfig must land on the path the batch state already expects.
	if !strings.Contains(runner.calls[2], "--config-name "+filepath.Base(item.Kubeconfig)) {
		t.Fatalf("kubeconfig file name does not match the batch state: %q", runner.calls[2])
	}

	manifest := runner.stdin[runner.calls[0]]
	if !strings.Contains(string(manifest), "kind: Cluster") || !strings.Contains(string(manifest), "kind: Namespace") {
		t.Fatalf("applied manifest is incomplete:\n%s", manifest)
	}

	// The manifest is also written to the artifacts directory for diagnosis.
	if _, err := os.Stat(filepath.Join(item.ArtifactDir, "cluster.yaml")); err != nil {
		t.Fatalf("cluster.yaml was not written to the artifacts directory: %v", err)
	}
}

// TestCreateSkipsLoadBalancerReconcileWhenNotUsed avoids paying for discovery on the common
// NodePort path.
func TestCreateSkipsLoadBalancerReconcileWhenNotUsed(t *testing.T) {
	config, item, target := createFixture(t, "")
	runner := newRecordingRunner()
	cli := CLI{K3KCLI: "k3kcli", Kubectl: "kubectl", Runner: runner}

	if err := cli.Create(context.Background(), item, target, config); err != nil {
		t.Fatal(err)
	}
	if call := runner.find("get svc"); call != "" {
		t.Fatalf("LoadBalancer discovery should not run for nodePort: %q", call)
	}
}

// TestCreatePatchesDiscoveredLoadBalancerAddress covers the reconcile: without the allocated
// address in tlsSANs, k3k falls back to tlsSANs[0] and issues a kubeconfig for the wrong host.
func TestCreatePatchesDiscoveredLoadBalancerAddress(t *testing.T) {
	config, item, target := createFixture(t, "tlsSANs: [\"lb.example.com\"]\nexpose:\n  loadBalancer: {}\n")
	runner := newRecordingRunner()
	runner.outputs["get svc"] = `{"items":[
		{"spec":{"type":"ClusterIP","selector":{"cluster":"k3k-ci-001","role":"server"}},"status":{}},
		{"spec":{"type":"LoadBalancer","selector":{"cluster":"k3k-ci-001","role":"server"}},
		 "status":{"loadBalancer":{"ingress":[{"ip":"203.0.113.7"}]}}}
	]}`
	runner.outputs["get cluster.k3k.io"] = `{"spec":{"tlsSANs":["lb.example.com","192.0.2.10"]}}`
	cli := CLI{K3KCLI: "k3kcli", Kubectl: "kubectl", Runner: runner}

	if err := cli.Create(context.Background(), item, target, config); err != nil {
		t.Fatal(err)
	}

	patch := runner.find("patch cluster.k3k.io/k3k-ci-001")
	if patch == "" {
		t.Fatalf("expected the discovered address to be patched in; calls were %v", runner.calls)
	}
	if !strings.Contains(patch, "203.0.113.7") {
		t.Fatalf("patch does not carry the discovered address: %q", patch)
	}
	// The user's hostname must remain first so it stays the kubeconfig server URL.
	if !strings.Contains(patch, `["lb.example.com","192.0.2.10","203.0.113.7"]`) {
		t.Fatalf("patch changed SAN ordering: %q", patch)
	}
	if runner.find("kubeconfig generate") == "" {
		t.Fatal("kubeconfig generation must still run after the reconcile")
	}
}

// TestCreateContinuesWhenNoLoadBalancerAddress implements the agreed behaviour: discover the
// address if possible, otherwise omit it rather than failing the cluster.
func TestCreateContinuesWhenNoLoadBalancerAddress(t *testing.T) {
	config, item, target := createFixture(t, "tlsSANs: [\"lb.example.com\"]\nexpose:\n  loadBalancer: {}\n")
	runner := newRecordingRunner()
	runner.outputs["get svc"] = `{"items":[
		{"spec":{"type":"LoadBalancer","selector":{"cluster":"k3k-ci-001","role":"server"}},
		 "status":{"loadBalancer":{"ingress":[]}}}
	]}`
	cli := CLI{K3KCLI: "k3kcli", Kubectl: "kubectl", Runner: runner}

	if err := cli.Create(context.Background(), item, target, config); err != nil {
		t.Fatalf("a missing LoadBalancer address must not fail the cluster: %v", err)
	}
	if patch := runner.find("patch cluster.k3k.io"); patch != "" {
		t.Fatalf("nothing should be patched when no address is found: %q", patch)
	}
	if runner.find("kubeconfig generate") == "" {
		t.Fatal("kubeconfig generation must still run")
	}
}

// TestCreateSkipsPatchWhenAddressAlreadyPresent avoids a pointless update and re-reconcile.
func TestCreateSkipsPatchWhenAddressAlreadyPresent(t *testing.T) {
	config, item, target := createFixture(t, "tlsSANs: [\"lb.example.com\", \"203.0.113.7\"]\nexpose:\n  loadBalancer: {}\n")
	runner := newRecordingRunner()
	runner.outputs["get svc"] = `{"items":[
		{"spec":{"type":"LoadBalancer","selector":{"cluster":"k3k-ci-001","role":"server"}},
		 "status":{"loadBalancer":{"ingress":[{"ip":"203.0.113.7"}]}}}
	]}`
	runner.outputs["get cluster.k3k.io"] = `{"spec":{"tlsSANs":["lb.example.com","203.0.113.7","192.0.2.10"]}}`
	cli := CLI{K3KCLI: "k3kcli", Kubectl: "kubectl", Runner: runner}

	if err := cli.Create(context.Background(), item, target, config); err != nil {
		t.Fatal(err)
	}
	if patch := runner.find("patch cluster.k3k.io"); patch != "" {
		t.Fatalf("address already present, no patch expected: %q", patch)
	}
}

func TestLoadBalancerAddressSelection(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "prefers an ip",
			body: `{"items":[{"spec":{"type":"LoadBalancer","selector":{"role":"server"}},"status":{"loadBalancer":{"ingress":[{"ip":"203.0.113.7","hostname":"lb.aws"}]}}}]}`,
			want: "203.0.113.7",
		},
		{
			name: "falls back to a hostname",
			body: `{"items":[{"spec":{"type":"LoadBalancer","selector":{"role":"server"}},"status":{"loadBalancer":{"ingress":[{"hostname":"lb.aws"}]}}}]}`,
			want: "lb.aws",
		},
		{
			name: "ignores services that are not the k3k server",
			body: `{"items":[{"spec":{"type":"LoadBalancer","selector":{"role":"agent"}},"status":{"loadBalancer":{"ingress":[{"ip":"203.0.113.9"}]}}}]}`,
			want: "",
		},
		{
			name: "ignores non-LoadBalancer services",
			body: `{"items":[{"spec":{"type":"ClusterIP","selector":{"role":"server"}},"status":{}}]}`,
			want: "",
		},
		{
			name: "tolerates unparsable output",
			body: `not json`,
			want: "",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := loadBalancerAddress([]byte(testCase.body)); got != testCase.want {
				t.Fatalf("loadBalancerAddress = %q, want %q", got, testCase.want)
			}
		})
	}
}
