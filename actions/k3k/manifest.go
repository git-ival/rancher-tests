package k3k

import (
	"bytes"
	"fmt"
	"strings"

	sigsyaml "sigs.k8s.io/yaml"
)

// Labels applied to every Cluster object k3kbulk creates.
const (
	BatchLabel  = "k3k-bulk.io/batch"
	TargetLabel = "k3k-bulk.io/target"
)

// clusterAPIVersion and clusterKind identify the k3k Cluster resource.
const (
	clusterAPIVersion = "k3k.io/v1beta1"
	clusterKind       = "Cluster"
)

// namespaceManifest is the serialized form of the namespace that holds a cluster.
type namespaceManifest struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   objectMeta `json:"metadata"`
}

type objectMeta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// SpecFor renders the clusterSpec for one cluster and applies every adjustment k3kbulk owns.
//
// k3kcli previously performed these adjustments implicitly; because k3kbulk now applies the
// manifest directly, they are reproduced here:
//
//   - deprecated top-level aliases are folded in
//   - a "+" in the version is replaced with "-" to form a valid image reference
//   - expose defaults to nodePort, because an unset expose yields a ClusterIP service whose
//     address is not reachable from the machine running k3kbulk
//   - the target's kubeconfig server is added to tlsSANs, with ordering chosen so that
//     tlsSANs[0] stays meaningful for the selected expose mode
func (c *Config) SpecFor(item *ClusterState, target Target) (*ClusterSpec, error) {
	spec, err := RenderSpec(c.RawSpecYAML, SpecContextFor(item, target, c))
	if err != nil {
		return nil, err
	}
	if err := c.ApplyDeprecatedAliases(spec); err != nil {
		return nil, err
	}
	if err := validateSpec(spec); err != nil {
		return nil, fmt.Errorf("cluster %s: %w", item.Name, err)
	}

	sanitizeVersion(spec)
	applyExposeDefault(spec)
	applyKubeconfigServerSAN(spec, target.KubeconfigServer)
	return spec, nil
}

// sanitizeVersion mirrors k3kcli: a K3s version containing "+" is not a valid Docker tag.
func sanitizeVersion(spec *ClusterSpec) {
	if strings.Contains(spec.Version, "+") {
		spec.Version = strings.ReplaceAll(spec.Version, "+", "-")
	}
}

// applyExposeDefault selects NodePort when the user configured no expose mode.
//
// Without it the k3k server service is a ClusterIP, and the generated kubeconfig would point
// at an address that is only routable inside the host cluster.
func applyExposeDefault(spec *ClusterSpec) {
	if spec.Expose == nil {
		spec.Expose = &ExposeConfig{NodePort: &NodePortConfig{}}
		return
	}
	if spec.Expose.Ingress == nil && spec.Expose.LoadBalancer == nil && spec.Expose.NodePort == nil {
		spec.Expose.NodePort = &NodePortConfig{}
	}
}

// applyKubeconfigServerSAN adds the host address to tlsSANs.
//
// Ordering matters because k3k derives the Ingress rule hosts from tlsSANs and falls back to
// tlsSANs[0] whenever the resolved service address is absent from the cluster status:
//
//   - NodePort: prepend, matching the behaviour k3kcli produced, so tlsSANs[0] is the reachable
//     host address.
//   - Ingress and LoadBalancer: append, so the user-supplied hostname stays at index 0 and
//     remains both the Ingress rule host and the kubeconfig server URL.
func applyKubeconfigServerSAN(spec *ClusterSpec, kubeconfigServer string) {
	if kubeconfigServer == "" {
		spec.TLSSANs = dedupeStrings(spec.TLSSANs)
		return
	}

	usesHostname := spec.Expose != nil && (spec.Expose.Ingress != nil || spec.Expose.LoadBalancer != nil)
	if usesHostname {
		spec.TLSSANs = dedupeStrings(append(append([]string{}, spec.TLSSANs...), kubeconfigServer))
		return
	}
	spec.TLSSANs = dedupeStrings(append([]string{kubeconfigServer}, spec.TLSSANs...))
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// RenderCluster produces the Namespace and Cluster manifests applied for one cluster.
//
// The namespace is included because k3kcli created it as a side effect of "cluster create",
// and k3kbulk no longer invokes that command.
func (c *Config) RenderCluster(item *ClusterState, target Target) ([]byte, error) {
	spec, err := c.SpecFor(item, target)
	if err != nil {
		return nil, err
	}
	return renderClusterManifest(item, target, spec, c.Batch)
}

func renderClusterManifest(item *ClusterState, target Target, spec *ClusterSpec, batch string) ([]byte, error) {
	namespace := namespaceManifest{
		APIVersion: "v1",
		Kind:       "Namespace",
		Metadata: objectMeta{
			Name: item.Namespace,
			Labels: map[string]string{
				BatchLabel:  batch,
				TargetLabel: target.Name,
			},
		},
	}

	specYAML, err := sigsyaml.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("render clusterSpec for %s: %w", item.Name, err)
	}

	cluster := map[string]any{
		"apiVersion": clusterAPIVersion,
		"kind":       clusterKind,
		"metadata": map[string]any{
			"name":      item.Name,
			"namespace": item.Namespace,
			"labels": map[string]string{
				BatchLabel:  batch,
				TargetLabel: target.Name,
			},
		},
	}

	specValue := map[string]any{}
	if err := sigsyaml.Unmarshal(specYAML, &specValue); err != nil {
		return nil, fmt.Errorf("render clusterSpec for %s: %w", item.Name, err)
	}
	cluster["spec"] = specValue

	namespaceYAML, err := sigsyaml.Marshal(namespace)
	if err != nil {
		return nil, fmt.Errorf("render namespace for %s: %w", item.Name, err)
	}
	clusterYAML, err := sigsyaml.Marshal(cluster)
	if err != nil {
		return nil, fmt.Errorf("render cluster for %s: %w", item.Name, err)
	}

	var manifest bytes.Buffer
	manifest.WriteString("---\n")
	manifest.Write(namespaceYAML)
	manifest.WriteString("---\n")
	manifest.Write(clusterYAML)
	return manifest.Bytes(), nil
}
