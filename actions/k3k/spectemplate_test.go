package k3k

import (
	"strings"
	"testing"
)

// TestSpecTemplateProducesPerClusterValues covers the reason templating exists: one shared
// clusterSpec block has to yield distinct Ingress hosts and node ports for each cluster in a
// batch, otherwise every cluster claims the same hostname and port.
func TestSpecTemplateProducesPerClusterValues(t *testing.T) {
	raw := []byte(`
tlsSANs:
- "{{ .Name }}.k3k.example.com"
expose:
  nodePort:
    serverPort: {{ add 30000 .Index }}
`)

	config := &Config{Batch: "k3k-ci"}
	seenHosts := map[string]bool{}
	seenPorts := map[int32]bool{}

	for _, name := range []string{"k3k-ci-001", "k3k-ci-002", "k3k-ci-003"} {
		item := &ClusterState{Name: name, Namespace: "k3k-" + name}
		spec, err := RenderSpec(raw, SpecContextFor(item, Target{Name: "host"}, config))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if len(spec.TLSSANs) != 1 {
			t.Fatalf("%s: expected one SAN, got %v", name, spec.TLSSANs)
		}
		if seenHosts[spec.TLSSANs[0]] {
			t.Fatalf("%s: duplicate Ingress host %q across the batch", name, spec.TLSSANs[0])
		}
		seenHosts[spec.TLSSANs[0]] = true

		port := *spec.Expose.NodePort.ServerPort
		if seenPorts[port] {
			t.Fatalf("%s: duplicate node port %d across the batch", name, port)
		}
		seenPorts[port] = true
	}

	if !seenHosts["k3k-ci-002.k3k.example.com"] {
		t.Fatalf("unexpected hosts: %v", seenHosts)
	}
	if !seenPorts[30002] {
		t.Fatalf("unexpected ports: %v", seenPorts)
	}
}

// TestSpecTemplateIsDeterministic guards resume safety: re-rendering the same cluster must
// produce an identical spec, because a retried create re-applies the manifest.
func TestSpecTemplateIsDeterministic(t *testing.T) {
	raw := []byte(`tlsSANs: ["{{ .Name }}-{{ .Index }}.example.com"]`)
	config := &Config{Batch: "k3k-ci"}
	item := &ClusterState{Name: "k3k-ci-007", Namespace: "k3k-k3k-ci-007"}
	target := Target{Name: "host"}

	first, err := RenderSpec(raw, SpecContextFor(item, target, config))
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderSpec(raw, SpecContextFor(item, target, config))
	if err != nil {
		t.Fatal(err)
	}

	if first.TLSSANs[0] != second.TLSSANs[0] {
		t.Fatalf("render is not deterministic: %q then %q", first.TLSSANs[0], second.TLSSANs[0])
	}
	if first.TLSSANs[0] != "k3k-ci-007-7.example.com" {
		t.Fatalf("index was not derived from the cluster name: %q", first.TLSSANs[0])
	}
}

// TestSpecTemplateRejectsUnknownVariable ensures a typo fails loudly rather than rendering an
// empty string into a hostname.
func TestSpecTemplateRejectsUnknownVariable(t *testing.T) {
	raw := []byte(`tlsSANs: ["{{ .Nmae }}.example.com"]`)
	config := &Config{Batch: "k3k-ci"}
	item := &ClusterState{Name: "k3k-ci-001"}

	_, err := RenderSpec(raw, SpecContextFor(item, Target{}, config))
	if err == nil {
		t.Fatal("expected an error for an unknown template variable")
	}
	if !strings.Contains(err.Error(), "Nmae") {
		t.Fatalf("error should name the bad variable: %v", err)
	}
}

// TestSpecTemplateContextFields checks that every documented variable resolves.
func TestSpecTemplateContextFields(t *testing.T) {
	raw := []byte(`serverArgs:
- "{{ .Name }}|{{ .Namespace }}|{{ .Batch }}|{{ .Index }}|{{ .Index0 }}|{{ .Target }}|{{ .TargetType }}|{{ .KubeconfigServer }}"`)

	config := &Config{Batch: "k3k-ci"}
	item := &ClusterState{Name: "k3k-ci-012", Namespace: "k3k-k3k-ci-012", TargetType: "imported"}
	target := Target{Name: "downstream-0-0", KubeconfigServer: "192.0.2.10"}

	spec, err := RenderSpec(raw, SpecContextFor(item, target, config))
	if err != nil {
		t.Fatal(err)
	}

	want := "k3k-ci-012|k3k-k3k-ci-012|k3k-ci|12|11|downstream-0-0|imported|192.0.2.10"
	if spec.ServerArgs[0] != want {
		t.Fatalf("context resolved to %q, want %q", spec.ServerArgs[0], want)
	}
}

// TestSpecTemplateFuncs exercises the supported helper functions.
func TestSpecTemplateFuncs(t *testing.T) {
	raw := []byte(`serverArgs: ["{{ add 1 2 }}-{{ sub 5 1 }}-{{ mul 3 3 }}-{{ mod 7 4 }}-{{ lower "AB" }}-{{ upper "cd" }}"]`)
	spec, err := RenderSpec(raw, SpecContext{Name: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ServerArgs[0] != "3-4-9-3-ab-CD" {
		t.Fatalf("unexpected function output: %q", spec.ServerArgs[0])
	}
}

// TestRenderSpecEmpty accepts a config with no clusterSpec block at all.
func TestRenderSpecEmpty(t *testing.T) {
	spec, err := RenderSpec(nil, SpecContext{})
	if err != nil {
		t.Fatal(err)
	}
	if spec == nil {
		t.Fatal("expected an empty spec, got nil")
	}
}
