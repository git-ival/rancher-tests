package k3k

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func generateForTest(t *testing.T, spec map[string]any) string {
	t.Helper()
	data, err := GenerateConfig(GenerateOptions{
		Batch: "k3k-ci", Count: 1, Parallelism: 4, PerTargetParallelism: 1,
		CreateTimeout: "10m", ArtifactsDir: "artifacts",
		Rancher: RancherConfig{
			Host: "rancher.example.com", TokenEnv: "TEST_RANCHER_TOKEN",
			Insecure: true, Workspace: "fleet-default",
		},
		Targets: []Target{{
			Name: "downstream-0-0", Type: "imported",
			Kubeconfig: "/downstream-0-0.yaml", KubeconfigServer: "192.0.2.10",
			Weight: 1, Parallelism: 1,
		}},
		ClusterSpec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func loadGenerated(t *testing.T, content string) (*Config, error) {
	t.Helper()
	t.Setenv("TEST_RANCHER_TOKEN", "token-id:secret")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(path)
}

// TestGeneratedConfigLoads is the baseline contract: what the generator writes must be
// accepted by the loader.
func TestGeneratedConfigLoads(t *testing.T) {
	config, err := loadGenerated(t, generateForTest(t, map[string]any{"mode": "shared", "servers": 3}))
	if err != nil {
		t.Fatal(err)
	}
	if *config.ClusterSpec.Servers != 3 {
		t.Fatalf("servers = %v", config.ClusterSpec.Servers)
	}
}

// TestGeneratedConfigCoversEverySpecField is the core of the requirement: every supported
// field must be present in the output, live when set and commented otherwise.
func TestGeneratedConfigCoversEverySpecField(t *testing.T) {
	content := generateForTest(t, map[string]any{"mode": "virtual", "servers": 3})

	fields, err := specFields(specStructType(), "")
	if err != nil {
		t.Fatal(err)
	}

	var check func(items []specField)
	check = func(items []specField) {
		for _, field := range items {
			// Match the key either live or commented out, at any indentation.
			pattern := regexp.MustCompile(`(?m)^\s*(# )?` + regexp.QuoteMeta(field.Name) + `:`)
			if !pattern.MatchString(content) {
				t.Errorf("generated config does not mention clusterSpec field %q", field.Path)
			}
			check(field.Children)
		}
	}
	check(fields)
}

// TestGeneratedPlaceholdersAreValid uncomments each commented leaf on its own and reloads the
// config, so a placeholder can never be syntactically or semantically broken.
//
// Fields are enabled one at a time because expose.ingress, expose.loadBalancer, and
// expose.nodePort are mutually exclusive by design.
func TestGeneratedPlaceholdersAreValid(t *testing.T) {
	content := generateForTest(t, nil)

	// Fields requiring extra context to be valid on their own.
	prerequisites := map[string]string{
		"ingressClassName": "  tlsSANs: [\"{{ .Name }}.example.com\"]\n",
		"annotations":      "  tlsSANs: [\"{{ .Name }}.example.com\"]\n",
	}

	lines := strings.Split(content, "\n")
	placeholder := regexp.MustCompile(`^(\s*)# ([a-zA-Z]+): (.+)$`)

	enabled := 0
	for index, line := range lines {
		match := placeholder.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		indent, key, value := match[1], match[2], match[3]

		// Only leaf scalars at the top level of clusterSpec are independently valid; nested
		// keys need their commented parent, which this test does not reconstruct.
		if len(indent) != 2 {
			continue
		}
		if _, documented := specDocs[key]; !documented {
			continue
		}

		t.Run(key, func(t *testing.T) {
			rebuilt := append([]string{}, lines...)
			rebuilt[index] = indent + key + ": " + value
			body := strings.Join(rebuilt, "\n")
			if extra, found := prerequisites[key]; found {
				body = strings.Replace(body, "clusterSpec:\n", "clusterSpec:\n"+extra, 1)
			}

			if _, err := loadGenerated(t, body); err != nil {
				t.Fatalf("uncommenting %q produced an invalid config: %v", key, err)
			}
		})
		enabled++
	}

	if enabled == 0 {
		t.Fatal("no commented placeholders were found to verify")
	}
}

// TestGeneratedCuratedExamplesAreValid checks the hand-written example blocks for the fields
// whose schemas are too large to expand by reflection.
func TestGeneratedCuratedExamplesAreValid(t *testing.T) {
	for path, doc := range specDocs {
		if len(doc.Example) == 0 {
			continue
		}
		t.Run(path, func(t *testing.T) {
			var body strings.Builder
			body.WriteString("clusterSpec:\n")
			for _, line := range doc.Example {
				body.WriteString("  " + line + "\n")
			}

			content := generateForTest(t, nil)
			content = content[:strings.Index(content, "clusterSpec:\n")] + body.String()

			if _, err := loadGenerated(t, content); err != nil {
				t.Fatalf("curated example for %q is invalid: %v", path, err)
			}
		})
	}
}

// TestGeneratorKeepsUnsetSiblingsVisible verifies partial-set recursion: setting one key of a
// struct must not hide the siblings the user did not set.
func TestGeneratorKeepsUnsetSiblingsVisible(t *testing.T) {
	content := generateForTest(t, map[string]any{
		"persistence": map[string]any{"type": "dynamic"},
	})

	if !strings.Contains(content, "\n  persistence:\n") {
		t.Fatalf("persistence should be live:\n%s", content)
	}
	if !strings.Contains(content, "\n    type: dynamic\n") {
		t.Fatalf("persistence.type should be live:\n%s", content)
	}
	if !strings.Contains(content, "# storageClassName:") {
		t.Fatalf("unset persistence.storageClassName should remain visible:\n%s", content)
	}
}

// TestGeneratorDoesNotExpandComplexTypes guards the depth policy. corev1.Affinity is roughly
// 875 lines of CRD schema; expanding it by reflection would bury the file.
func TestGeneratorDoesNotExpandComplexTypes(t *testing.T) {
	content := generateForTest(t, nil)

	if strings.Contains(content, "requiredDuringSchedulingIgnoredDuringExecution:") &&
		!strings.Contains(content, "#   nodeAffinity:") {
		t.Fatal("affinity appears to have been expanded by reflection rather than by example")
	}
	if lines := strings.Count(content, "\n"); lines > 260 {
		t.Fatalf("generated config is %d lines; the depth policy is not holding", lines)
	}
}

func TestLoadSpecValues(t *testing.T) {
	spec, err := LoadSpecValues("", []string{
		"mode=virtual",
		"servers=3",
		"persistence.type=dynamic",
		`nodeSelector={"disktype":"ssd"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	if spec["mode"] != "virtual" {
		t.Fatalf("mode = %v", spec["mode"])
	}
	if spec["servers"] != float64(3) {
		t.Fatalf("servers = %v (%T)", spec["servers"], spec["servers"])
	}
	persistence, ok := spec["persistence"].(map[string]any)
	if !ok || persistence["type"] != "dynamic" {
		t.Fatalf("dotted key was not nested: %v", spec["persistence"])
	}
	selector, ok := spec["nodeSelector"].(map[string]any)
	if !ok || selector["disktype"] != "ssd" {
		t.Fatalf("inline mapping was not parsed: %v", spec["nodeSelector"])
	}
}

func TestLoadSpecValuesRejectsUnsupportedField(t *testing.T) {
	if _, err := LoadSpecValues("", []string{"customCAs.enabled=true"}); err == nil {
		t.Fatal("expected an unsupported clusterSpec field to be rejected")
	}
}

func TestLoadTargetsJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.json")
	body := `[{"name":"a","type":"imported","kubeconfig":"/a.yaml","kubeconfigServer":"192.0.2.1"}]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	targets, err := LoadTargetsJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected one target, got %d", len(targets))
	}
	if targets[0].Weight != 1 || targets[0].Parallelism != 1 {
		t.Fatalf("defaults were not applied: %#v", targets[0])
	}
}
