package k3k

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsAndWeightedTargets(t *testing.T) {
	t.Setenv("TEST_RANCHER_TOKEN", "token-id:secret")
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
batch: test
count: 3
rancher:
  host: rancher.example.com
  tokenEnv: TEST_RANCHER_TOKEN
targets:
  - name: a
    kubeconfig: /a
    kubeconfigServer: 192.0.2.1
    weight: 2
  - name: b
    kubeconfig: /b
    kubeconfigServer: 192.0.2.2
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != "shared" || config.PersistenceType != "ephemeral" || config.Rancher.Token != "token-id:secret" {
		t.Fatalf("defaults or token not applied: %#v", config)
	}
	targets := weightedTargets(config.Targets)
	if len(targets) != 3 || targets[0].Name != "a" || targets[1].Name != "a" || targets[2].Name != "b" {
		t.Fatalf("unexpected weighted targets: %#v", targets)
	}
}
