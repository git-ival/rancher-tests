package envplan

import (
	"fmt"
	"strings"

	"github.com/rancher/tests/internal/agenticqa/types"
)

// SpecRefinementResult is the LLM's adjusted machine specs for one environment
// group, keyed by node-pool role signature (e.g. "etcd+controlplane+worker").
type SpecRefinementResult struct {
	Pools []struct {
		Roles     string `json:"roles"`
		VCPUs     int    `json:"vcpus"`
		MemoryGiB int    `json:"memory_gib"`
		DiskGiB   int    `json:"disk_gib"`
		Rationale string `json:"rationale"`
	} `json:"pools"`
	Notes string `json:"notes"`
}

// BuildSpecRefinementSystemPrompt returns the system prompt for refining the
// heuristic machine specs of a single environment group.
func BuildSpecRefinementSystemPrompt(projectDisplayName string) string {
	return fmt.Sprintf(`You are a test-infrastructure capacity expert for the %s
project. You are given the node pools of a downstream test cluster, each with a
heuristic baseline machine spec (vCPUs, memory in GiB, disk in GiB), plus the
list of workloads the tests deploy.

Refine each pool's spec to the MINIMUM size that will reliably run the tests:
- Never go below the heuristic baseline for a pool unless it is clearly
  oversized; prefer to keep or modestly increase it.
- Increase memory for control-plane/etcd pools if many CRDs/controllers or
  large clusters are implied.
- Increase worker vCPU/memory/disk when workloads are numerous or heavy
  (statefulsets, autoscaling, large deployments).
- Keep sizes realistic and cost-conscious; do not exceed %d vCPUs / %d GiB /
  %d GiB disk per node.

Respond ONLY with JSON matching this schema. Echo each pool back by its exact
"roles" string:
{
  "pools": [
    {"roles": "string", "vcpus": int, "memory_gib": int, "disk_gib": int, "rationale": "string"}
  ],
  "notes": "string"
}`, projectDisplayName, maxHeuristicVCPUs, maxHeuristicMemGiB, maxHeuristicDiskGiB)
}

// BuildSpecRefinementUserMessage describes the group's pools and workloads for
// the LLM.
func BuildSpecRefinementUserMessage(g types.EnvironmentGroup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Environment group: %s\n", g.Name)
	fmt.Fprintf(&b, "Total downstream nodes: %d\n\n", g.Cluster.TotalNodes)

	b.WriteString("Node pools (with heuristic baseline specs):\n")
	for _, p := range g.Cluster.NodePools {
		roles := roleLabel(p)
		spec := p.Spec
		if spec == nil {
			fmt.Fprintf(&b, "- roles=%s quantity=%d (no baseline)\n", roles, p.Quantity)
			continue
		}
		fmt.Fprintf(&b, "- roles=%s quantity=%d baseline: %d vCPU, %d GiB mem, %d GiB disk\n",
			roles, p.Quantity, spec.VCPUs, spec.MemoryGiB, spec.DiskGiB)
	}

	if len(g.Charts) > 0 {
		b.WriteString("\nHelm charts installed by the tests (dominant resource driver):\n")
		for _, c := range g.Charts {
			if c.Footprint != nil {
				fmt.Fprintf(&b, "- %s: ~%dm CPU, ~%d MiB mem (%s)\n",
					c.Name, c.Footprint.CPUMillis, c.Footprint.MemoryMiB, c.Footprint.Source)
			} else {
				fmt.Fprintf(&b, "- %s: footprint unknown\n", c.Name)
			}
		}
	}

	if len(g.Workloads) > 0 {
		b.WriteString("\nWorkloads deployed by the tests:\n")
		for _, w := range g.Workloads {
			name := w.Name
			if name == "" {
				name = "(unnamed)"
			}
			fmt.Fprintf(&b, "- %s %s: %s\n", w.Kind, name, w.Description)
		}
	} else if len(g.Charts) == 0 {
		b.WriteString("\nNo workloads detected.\n")
	}

	b.WriteString("\nReturn refined specs per pool, keyed by the exact roles string shown above.")
	return b.String()
}

// ApplySpecRefinement overlays an LLM SpecRefinementResult onto a group's node
// pools, matching by role signature. Refined values never drop below the
// heuristic baseline (the LLM is advisory, the baseline is a floor). Pools the
// LLM omits keep their heuristic spec. Returns the number of pools updated.
func ApplySpecRefinement(g *types.EnvironmentGroup, res SpecRefinementResult) int {
	bySig := map[string]int{} // role signature -> pool index
	for i := range g.Cluster.NodePools {
		bySig[roleLabel(g.Cluster.NodePools[i])] = i
	}

	updated := 0
	for _, rp := range res.Pools {
		idx, ok := bySig[normalizeRoles(rp.Roles)]
		if !ok {
			continue
		}
		p := &g.Cluster.NodePools[idx]
		base := p.Spec
		refined := types.MachineSpec{
			VCPUs:     rp.VCPUs,
			MemoryGiB: rp.MemoryGiB,
			DiskGiB:   rp.DiskGiB,
			Source:    types.SpecSourceLLM,
			Rationale: rp.Rationale,
		}
		// Enforce the heuristic floor.
		if base != nil {
			if refined.VCPUs < base.VCPUs {
				refined.VCPUs = base.VCPUs
			}
			if refined.MemoryGiB < base.MemoryGiB {
				refined.MemoryGiB = base.MemoryGiB
			}
			if refined.DiskGiB < base.DiskGiB {
				refined.DiskGiB = base.DiskGiB
			}
		}
		clampSpec(&refined)
		p.Spec = &refined
		updated++
	}
	return updated
}

// normalizeRoles canonicalises an LLM-returned roles string ("worker, etcd",
// "etcd+worker", "Worker") to the same "+"-joined sorted lowercase form that
// roleLabel produces, so matching is robust to formatting differences.
func normalizeRoles(s string) string {
	repl := strings.NewReplacer(",", " ", "+", " ", "/", " ")
	parts := strings.Fields(strings.ToLower(repl.Replace(s)))
	// roleLabel sorts; build a NodeRequirement-equivalent ordering.
	var etcd, cp, worker, windows bool
	for _, p := range parts {
		switch p {
		case types.RoleEtcd:
			etcd = true
		case types.RoleControlPlane, "control-plane", "cp":
			cp = true
		case types.RoleWorker:
			worker = true
		case types.RoleWindows:
			windows = true
		}
	}
	return roleLabel(types.NodeRequirement{Etcd: etcd, ControlPlane: cp, Worker: worker, Windows: windows})
}
