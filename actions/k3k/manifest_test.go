package k3k

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"
)

func testConfig(t *testing.T, spec string) *Config {
	t.Helper()
	config := &Config{Batch: "k3k-ci", Count: 1, RawSpecYAML: []byte(spec), RawClusterSpec: map[string]any{}}
	if spec != "" {
		if err := sigsyaml.Unmarshal([]byte(spec), &config.RawClusterSpec); err != nil {
			t.Fatal(err)
		}
	}
	config.rawTopLevel = map[string]any{}
	config.Mode = ModeShared
	config.Servers = 1
	config.PersistenceType = PersistenceEphemeral
	return config
}

func testItem() *ClusterState {
	return &ClusterState{Name: "k3k-ci-001", Namespace: "k3k-k3k-ci-001"}
}

func testTarget() Target {
	return Target{Name: "downstream-0-0", Type: "imported", KubeconfigServer: "192.0.2.10"}
}

// TestExposeDefaultsToNodePort covers the behaviour k3kcli used to provide implicitly. Without
// it the k3k server Service is a ClusterIP and the generated kubeconfig points at an address
// that is not routable from the machine running k3kbulk.
func TestExposeDefaultsToNodePort(t *testing.T) {
	config := testConfig(t, "")

	spec, err := config.SpecFor(testItem(), testTarget())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Expose == nil || spec.Expose.NodePort == nil {
		t.Fatalf("expose did not default to nodePort: %#v", spec.Expose)
	}
	if spec.Expose.Ingress != nil || spec.Expose.LoadBalancer != nil {
		t.Fatalf("expose default should not set other modes: %#v", spec.Expose)
	}
}

// TestExposeDefaultDoesNotOverrideIngress ensures the default only fills an empty expose.
func TestExposeDefaultDoesNotOverrideIngress(t *testing.T) {
	config := testConfig(t, "tlsSANs: [\"a.example.com\"]\nexpose:\n  ingress:\n    ingressClassName: nginx\n")

	spec, err := config.SpecFor(testItem(), testTarget())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Expose.NodePort != nil {
		t.Fatalf("nodePort must not be added when ingress is configured: %#v", spec.Expose)
	}
	if spec.Expose.Ingress.IngressClassName != "nginx" {
		t.Fatalf("ingress config was lost: %#v", spec.Expose.Ingress)
	}
}

// TestTLSSANOrdering pins the ordering rules.
//
// k3k derives Ingress rule hosts from tlsSANs and falls back to tlsSANs[0] when the resolved
// service address is absent from the cluster status, so index 0 has to hold the right value
// for the selected expose mode.
func TestTLSSANOrdering(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want []string
	}{
		{
			name: "nodePort prepends the host address, matching k3kcli",
			spec: "tlsSANs: [\"extra.example.com\"]\n",
			want: []string{"192.0.2.10", "extra.example.com"},
		},
		{
			name: "ingress appends so the user hostname stays first",
			spec: "tlsSANs: [\"a.example.com\"]\nexpose:\n  ingress:\n    ingressClassName: nginx\n",
			want: []string{"a.example.com", "192.0.2.10"},
		},
		{
			name: "loadBalancer appends so the user hostname stays first",
			spec: "tlsSANs: [\"lb.example.com\"]\nexpose:\n  loadBalancer: {}\n",
			want: []string{"lb.example.com", "192.0.2.10"},
		},
		{
			name: "no configured SANs yields only the host address",
			spec: "",
			want: []string{"192.0.2.10"},
		},
		{
			name: "an already present host address is not duplicated",
			spec: "tlsSANs: [\"192.0.2.10\", \"extra.example.com\"]\n",
			want: []string{"192.0.2.10", "extra.example.com"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := testConfig(t, testCase.spec)
			spec, err := config.SpecFor(testItem(), testTarget())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(spec.TLSSANs, ",") != strings.Join(testCase.want, ",") {
				t.Fatalf("tlsSANs = %v, want %v", spec.TLSSANs, testCase.want)
			}
		})
	}
}

// TestVersionSanitization mirrors k3kcli: a "+" is not valid in a Docker image tag.
func TestVersionSanitization(t *testing.T) {
	config := testConfig(t, "version: v1.36.3+k3s1\n")

	spec, err := config.SpecFor(testItem(), testTarget())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != "v1.36.3-k3s1" {
		t.Fatalf("version = %q, want v1.36.3-k3s1", spec.Version)
	}
}

// TestRenderClusterManifest checks the applied document set: a namespace, because k3kcli
// created it as a side effect of "cluster create", and the Cluster itself with batch labels.
func TestRenderClusterManifest(t *testing.T) {
	config := testConfig(t, "mode: virtual\nservers: 3\nagents: 3\n")
	config.rawTopLevel = map[string]any{}
	config.Mode = ModeVirtual

	manifest, err := config.RenderCluster(testItem(), testTarget())
	if err != nil {
		t.Fatal(err)
	}

	documents := splitYAMLDocuments(t, manifest)
	if len(documents) != 2 {
		t.Fatalf("expected a namespace and a cluster document, got %d", len(documents))
	}

	namespace := documents[0]
	if namespace["kind"] != "Namespace" {
		t.Fatalf("first document is %v, want Namespace", namespace["kind"])
	}
	namespaceMeta := namespace["metadata"].(map[string]any)
	if namespaceMeta["name"] != "k3k-k3k-ci-001" {
		t.Fatalf("namespace name = %v", namespaceMeta["name"])
	}

	cluster := documents[1]
	if cluster["apiVersion"] != clusterAPIVersion || cluster["kind"] != clusterKind {
		t.Fatalf("unexpected cluster type meta: %v %v", cluster["apiVersion"], cluster["kind"])
	}
	clusterMeta := cluster["metadata"].(map[string]any)
	if clusterMeta["name"] != "k3k-ci-001" || clusterMeta["namespace"] != "k3k-k3k-ci-001" {
		t.Fatalf("unexpected cluster metadata: %v", clusterMeta)
	}
	labels := clusterMeta["labels"].(map[string]any)
	if labels[BatchLabel] != "k3k-ci" || labels[TargetLabel] != "downstream-0-0" {
		t.Fatalf("batch labels missing: %v", labels)
	}

	clusterSpec := cluster["spec"].(map[string]any)
	if clusterSpec["mode"] != "virtual" {
		t.Fatalf("mode = %v", clusterSpec["mode"])
	}
	if clusterSpec["servers"] != float64(3) && clusterSpec["servers"] != 3 {
		t.Fatalf("servers = %v (%T)", clusterSpec["servers"], clusterSpec["servers"])
	}
	if _, found := clusterSpec["expose"]; !found {
		t.Fatal("expose default missing from the rendered manifest")
	}
}

// TestRenderClusterOmitsUnsetFields keeps the manifest minimal so the CRD applies its own
// defaults rather than receiving explicit nulls.
func TestRenderClusterOmitsUnsetFields(t *testing.T) {
	config := testConfig(t, "")

	manifest, err := config.RenderCluster(testItem(), testTarget())
	if err != nil {
		t.Fatal(err)
	}

	for _, unwanted := range []string{"null", "serverAffinity", "agentAffinity", "serverResources", "workerResources", "nodeSelector"} {
		if strings.Contains(string(manifest), unwanted) {
			t.Fatalf("manifest should not mention %q:\n%s", unwanted, manifest)
		}
	}
}

// TestRenderClusterIsConcurrencySafe covers the orchestrator's real usage: clusters are
// rendered from goroutines bounded by parallelism, all sharing one Config.
func TestRenderClusterIsConcurrencySafe(t *testing.T) {
	config := testConfig(t, "tlsSANs: [\"{{ .Name }}.example.com\"]\n")
	config.rawTopLevel = map[string]any{"mode": "shared", "servers": 1, "persistenceType": "ephemeral"}

	var group sync.WaitGroup
	errs := make(chan error, 16)
	for index := 1; index <= 16; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			name := fmt.Sprintf("k3k-ci-%03d", index)
			item := &ClusterState{Name: name, Namespace: "k3k-" + name}
			manifest, err := config.RenderCluster(item, testTarget())
			if err != nil {
				errs <- err
				return
			}
			if !strings.Contains(string(manifest), name+".example.com") {
				errs <- fmt.Errorf("%s: manifest rendered the wrong hostname", name)
			}
		}(index)
	}
	group.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

func splitYAMLDocuments(t *testing.T, manifest []byte) []map[string]any {
	t.Helper()
	var documents []map[string]any
	for _, chunk := range strings.Split(string(manifest), "---\n") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		document := map[string]any{}
		if err := sigsyaml.Unmarshal([]byte(chunk), &document); err != nil {
			t.Fatalf("parse manifest document: %v\n%s", err, chunk)
		}
		documents = append(documents, document)
	}
	return documents
}
