package k3k

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// exampleDir holds the runnable configurations shipped with the command.
const exampleDir = "../../validation/pipeline/k3kbulk/examples"

// TestShippedExamplesAreValid loads every example configuration and renders a manifest for
// each cluster in its batch, so a documented example cannot drift out of sync with the loader,
// the validation rules, or the manifest renderer.
func TestShippedExamplesAreValid(t *testing.T) {
	t.Setenv("RANCHER_TOKEN", "token-id:secret")

	paths, err := filepath.Glob(filepath.Join(exampleDir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no example configurations found in %s", exampleDir)
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			config, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			// Discovery-based examples resolve their targets at run time; supply one so the
			// render path can be exercised offline.
			targets := config.Targets
			if len(targets) == 0 {
				targets = []Target{{
					Name: "downstream-0-0", Type: "imported",
					Kubeconfig: "/downstream-0-0.yaml", KubeconfigServer: "192.0.2.10",
					Weight: 1, Parallelism: 1,
				}}
			}

			seenSANs := map[string]string{}
			seenPorts := map[int32]string{}

			for index := 1; index <= config.Count; index++ {
				name := fmt.Sprintf("%s-%03d", config.Batch, index)
				item := &ClusterState{Name: name, Namespace: "k3k-" + name}
				target := targets[(index-1)%len(targets)]

				manifest, err := config.RenderCluster(item, target)
				if err != nil {
					t.Fatalf("render %s: %v", name, err)
				}
				if !strings.Contains(string(manifest), "kind: Cluster") {
					t.Fatalf("render %s produced no Cluster document", name)
				}

				spec, err := config.SpecFor(item, target)
				if err != nil {
					t.Fatal(err)
				}

				// An Ingress claims tlsSANs[0] as its host, so it must be unique per cluster.
				if spec.Expose != nil && spec.Expose.Ingress != nil {
					host := spec.TLSSANs[0]
					if previous, clash := seenSANs[host]; clash {
						t.Fatalf("%s and %s both claim Ingress host %q", previous, name, host)
					}
					seenSANs[host] = name
				}

				// Clusters sharing a host must not request the same fixed node port.
				if spec.Expose != nil && spec.Expose.NodePort != nil && spec.Expose.NodePort.ServerPort != nil {
					port := *spec.Expose.NodePort.ServerPort
					if previous, clash := seenPorts[port]; clash {
						t.Fatalf("%s and %s both request node port %d", previous, name, port)
					}
					seenPorts[port] = name
				}
			}
		})
	}
}
