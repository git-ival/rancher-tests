package k3k

import (
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"
)

// TestUseCaseSpecsRoundTrip is the drift guard between the local scoped ClusterSpec type and
// the real k3k CRD.
//
// Each case is the spec block from a documented k3k "create a virtual cluster" use case,
// copied verbatim. Decoding must accept it strictly and re-encoding must reproduce it, so a
// field the CRD renames or reshapes shows up as a test failure rather than as a manifest the
// k3k API server silently rejects at runtime.
func TestUseCaseSpecsRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		spec string
	}{
		{
			name: "expose a basic virtual cluster through ingress",
			spec: `
tlsSANs:
- my-cluster.example.com
expose:
  ingress:
    ingressClassName: nginx
    annotations:
      nginx.ingress.kubernetes.io/ssl-passthrough: "true"
      nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"
      nginx.ingress.kubernetes.io/ssl-redirect: "HTTPS"
`,
		},
		{
			name: "persistent storage",
			spec: `
persistence:
  type: dynamic
  storageClassName: local-path
  storageRequestSize: 30Gi
`,
		},
		{
			name: "highly available in shared mode",
			spec: `
servers: 3
`,
		},
		{
			name: "highly available in virtual mode",
			spec: `
mode: virtual
servers: 3
agents: 3
`,
		},
		{
			name: "ephemeral",
			spec: `
persistence:
  type: ephemeral
`,
		},
		{
			name: "custom kubernetes version",
			spec: `
version: v1.36.2-k3s1
`,
		},
		{
			name: "custom resource limits",
			spec: `
mode: virtual
serverLimit:
  cpu: "1"
  memory: 2Gi
workerLimit:
  cpu: "1"
  memory: 2Gi
`,
		},
		{
			name: "specific host nodes",
			spec: `
nodeSelector:
  disktype: ssd
`,
		},
		{
			name: "behind an http proxy",
			spec: `
serverEnvs:
- name: HTTP_PROXY
  value: http://abc.xyz
agentEnvs:
- name: HTTP_PROXY
  value: http://abc.xyz
`,
		},
		{
			name: "server and agent args",
			spec: `
serverArgs:
- --tls-san=example.com
agentArgs:
- --node-name=my-agent-node
`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			spec := &ClusterSpec{}
			if err := sigsyaml.UnmarshalStrict([]byte(testCase.spec), spec); err != nil {
				t.Fatalf("decode: %v", err)
			}

			encoded, err := sigsyaml.Marshal(spec)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}

			var want, got map[string]any
			if err := sigsyaml.Unmarshal([]byte(testCase.spec), &want); err != nil {
				t.Fatal(err)
			}
			if err := sigsyaml.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			assertDeepEqualYAML(t, want, got)
		})
	}
}

// TestAffinityAndResourcesRoundTrip covers the fields whose types come from corev1.
//
// These carry only JSON tags, so they are the fields that would be silently mangled if the
// spec were parsed with gopkg.in/yaml.v3 like the rest of the config file.
func TestAffinityAndResourcesRoundTrip(t *testing.T) {
	source := `
serverAffinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
      - matchExpressions:
        - key: disktype
          operator: In
          values:
          - ssd
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 100
      podAffinityTerm:
        topologyKey: kubernetes.io/hostname
        labelSelector:
          matchLabels:
            role: server
agentAffinity:
  nodeAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 50
      preference:
        matchExpressions:
        - key: zone
          operator: In
          values:
          - us-west-1a
serverResources:
  limits:
    cpu: "2"
    memory: 4Gi
  requests:
    cpu: "1"
    memory: 2Gi
workerResources:
  limits:
    cpu: "2"
    memory: 4Gi
`

	spec := &ClusterSpec{}
	if err := sigsyaml.UnmarshalStrict([]byte(source), spec); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if spec.ServerAffinity == nil || spec.ServerAffinity.NodeAffinity == nil {
		t.Fatal("serverAffinity.nodeAffinity was not decoded")
	}
	required := spec.ServerAffinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil || len(required.NodeSelectorTerms) != 1 {
		t.Fatalf("nodeSelectorTerms not decoded: %#v", required)
	}
	if got := required.NodeSelectorTerms[0].MatchExpressions[0].Key; got != "disktype" {
		t.Fatalf("matchExpressions key = %q, want disktype", got)
	}
	if spec.ServerAffinity.PodAntiAffinity == nil {
		t.Fatal("serverAffinity.podAntiAffinity was not decoded")
	}
	if spec.AgentAffinity == nil || spec.AgentAffinity.NodeAffinity == nil {
		t.Fatal("agentAffinity.nodeAffinity was not decoded")
	}
	if spec.ServerResources == nil || spec.ServerResources.Limits.Cpu().String() != "2" {
		t.Fatalf("serverResources limits not decoded: %#v", spec.ServerResources)
	}
	if spec.ServerResources.Requests.Memory().String() != "2Gi" {
		t.Fatalf("serverResources requests not decoded: %#v", spec.ServerResources.Requests)
	}
	if spec.WorkerResources == nil || spec.WorkerResources.Limits.Memory().String() != "4Gi" {
		t.Fatalf("workerResources not decoded: %#v", spec.WorkerResources)
	}

	encoded, err := sigsyaml.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := sigsyaml.Unmarshal([]byte(source), &want); err != nil {
		t.Fatal(err)
	}
	if err := sigsyaml.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	assertDeepEqualYAML(t, want, got)
}

// TestUnsupportedSpecFieldsRejected asserts that fields outside the supported subset produce
// a clear error instead of being silently ignored.
func TestUnsupportedSpecFieldsRejected(t *testing.T) {
	unsupported := []string{
		"sync:\n  services:\n    enabled: true\n",
		"customCAs:\n  enabled: true\n",
		"secretMounts:\n- secretName: example\n",
		"podSecurityContext:\n  runAsUser: 1000\n",
		"clusterCIDR: 10.42.0.0/16\n",
		"mirrorHostNodes: true\n",
	}

	for _, source := range unsupported {
		field := strings.SplitN(source, ":", 2)[0]
		t.Run(field, func(t *testing.T) {
			if err := sigsyaml.UnmarshalStrict([]byte(source), &ClusterSpec{}); err == nil {
				t.Fatalf("expected %q to be rejected as unsupported", field)
			}
		})
	}
}

// TestEverySpecFieldIsDocumented keeps the generator's documentation table in step with the
// type, so a new field cannot be added without a description for the generated config.
func TestEverySpecFieldIsDocumented(t *testing.T) {
	fields, err := specFields(specStructType(), "")
	if err != nil {
		t.Fatal(err)
	}

	var missing []string
	var walk func(items []specField)
	walk = func(items []specField) {
		for _, field := range items {
			if _, found := specDocs[field.Path]; !found {
				missing = append(missing, field.Path)
			}
			walk(field.Children)
		}
	}
	walk(fields)

	if len(missing) > 0 {
		t.Fatalf("clusterSpec fields missing from specDocs: %s", strings.Join(missing, ", "))
	}
}

func assertDeepEqualYAML(t *testing.T, want, got map[string]any) {
	t.Helper()
	wantYAML, err := sigsyaml.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	gotYAML, err := sigsyaml.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(wantYAML) != string(gotYAML) {
		t.Fatalf("spec did not round-trip\n--- want ---\n%s\n--- got ---\n%s", wantYAML, gotYAML)
	}
}
