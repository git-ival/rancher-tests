package envplan

import (
	"fmt"

	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	// llmTestSourceMaxLen is the maximum number of characters of a test file's
	// source code sent to the LLM for environment inference. Longer files are
	// truncated at this limit.
	llmTestSourceMaxLen = 40000
)

// LLMResult is the JSON schema the LLM returns for a single test file whose
// environment requirements could not be derived statically.
type LLMResult struct {
	NodePools []struct {
		Etcd         bool   `json:"etcd"`
		ControlPlane bool   `json:"controlplane"`
		Worker       bool   `json:"worker"`
		Windows      bool   `json:"windows"`
		Quantity     int    `json:"quantity"`
		Description  string `json:"description"`
	} `json:"node_pools"`
	KubernetesDistro  string `json:"kubernetes_distro"`
	KubernetesVersion string `json:"kubernetes_version"`
	CNI               string `json:"cni"`
	Provider          string `json:"provider"`
	NodeProvider      string `json:"node_provider"`
	PSACT             string `json:"psact"`
	Hardened          bool   `json:"hardened"`
	Downstream        bool   `json:"downstream"`
	Networking        struct {
		LocalClusterAuthEndpoint bool   `json:"local_cluster_auth_endpoint"`
		StackPreference          string `json:"stack_preference"`
	} `json:"networking"`
	Workloads []struct {
		Kind        string `json:"kind"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"workloads"`
	Reasoning string `json:"reasoning"`
}

// ToClusterRequirement converts an LLMResult into the canonical types used by
// the plan.
func (r LLMResult) ToClusterRequirement() (types.ClusterRequirement, []types.WorkloadRequirement) {
	cluster := types.ClusterRequirement{
		KubernetesDistro:  r.KubernetesDistro,
		KubernetesVersion: r.KubernetesVersion,
		CNI:               r.CNI,
		Provider:          r.Provider,
		NodeProvider:      r.NodeProvider,
		PSACT:             r.PSACT,
		Hardened:          r.Hardened,
		Downstream:        r.Downstream,
	}
	for _, p := range r.NodePools {
		qty := p.Quantity
		if qty < 1 {
			qty = 1
		}
		cluster.NodePools = append(cluster.NodePools, types.NodeRequirement{
			Etcd:         p.Etcd,
			ControlPlane: p.ControlPlane,
			Worker:       p.Worker,
			Windows:      p.Windows,
			Quantity:     qty,
			Description:  p.Description,
		})
	}
	if len(cluster.NodePools) > 0 {
		cluster.Downstream = true
	}
	cluster.TotalNodes = totalNodes(cluster.NodePools)

	if r.Networking.LocalClusterAuthEndpoint || r.Networking.StackPreference != "" {
		cluster.Networking = &types.NetworkingRequirement{
			LocalClusterAuthEndpoint: r.Networking.LocalClusterAuthEndpoint,
			StackPreference:          r.Networking.StackPreference,
		}
	}

	var workloads []types.WorkloadRequirement
	for _, w := range r.Workloads {
		workloads = append(workloads, types.WorkloadRequirement{
			Kind:        w.Kind,
			Name:        w.Name,
			Description: w.Description,
		})
	}
	return cluster, workloads
}

// BuildLLMSystemPrompt returns the system prompt for inferring a single test
// file's minimum viable environment. projectDisplayName personalises it.
func BuildLLMSystemPrompt(projectDisplayName string) string {
	return fmt.Sprintf(`You are a test-infrastructure expert for the %s project.
Given the source of a single Go integration/e2e test file, determine the
MINIMUM VIABLE downstream test environment required to run it successfully.

Consider:
- How many nodes are needed and what roles each must have (etcd, controlplane,
  worker, windows). Most provisioning tests need at least one all-roles node.
  Some need separate etcd (often 3), controlplane (often 2) and worker pools.
- Whether a downstream cluster is required at all, or only the local/management
  cluster (e.g. pure RBAC or settings tests may not provision downstream).
- The Kubernetes distro (rke2, k3s, rke1) and version if pinned.
- The CNI (calico, cilium, canal, etc.) if the test pins one.
- The cloud/node provider (aws, azure, harvester, vsphere, etc.) if required.
- PSACT (rancher-privileged/restricted/baseline) and hardened/CIS requirements.
- Special networking: LocalClusterAuthEndpoint (ACE), dual-stack/ipv6.
- Workloads/deployments the test creates that must succeed (deployment, pod,
  daemonset, statefulset, ingress, etc.).

Prefer the SMALLEST environment that can still run the test. If a dimension is
not constrained by the test, leave it empty/false so the pipeline default
applies. Respond ONLY with a JSON object matching this schema:
{
  "node_pools": [{"etcd": bool, "controlplane": bool, "worker": bool, "windows": bool, "quantity": int, "description": "string"}],
  "kubernetes_distro": "rke2|k3s|rke1|",
  "kubernetes_version": "string",
  "cni": "string",
  "provider": "string",
  "node_provider": "string",
  "psact": "string",
  "hardened": bool,
  "downstream": bool,
  "networking": {"local_cluster_auth_endpoint": bool, "stack_preference": "string"},
  "workloads": [{"kind": "string", "name": "string", "description": "string"}],
  "reasoning": "string"
}`, projectDisplayName)
}

// BuildLLMUserMessage returns the user message containing the test file path
// and (truncated) source for LLM inference.
func BuildLLMUserMessage(relPath, source string) string {
	if len(source) > llmTestSourceMaxLen {
		source = source[:llmTestSourceMaxLen] + "\n... (truncated)"
	}
	return fmt.Sprintf("Test file: %s\n\nSource:\n%s", relPath, source)
}
