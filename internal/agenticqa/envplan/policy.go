package envplan

import (
	"fmt"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/types"
)

// ApplySizingPolicy shapes a downstream cluster's node pools according to a
// sizing target spec: it raises per-role node counts to the configured floors,
// enforces odd etcd counts for quorum, clamps per-node recommended specs to the
// configured caps, and applies the total-node cap (HA floors win over the cap,
// emitting a warning). It returns the adjusted cluster and any warnings.
func ApplySizingPolicy(cluster types.ClusterRequirement, spec envconfig.SizingTargetSpec) (types.ClusterRequirement, []string) {
	pools, warnings := applyPolicyToPools(cluster.NodePools, spec, "downstream cluster")
	cluster.NodePools = pools
	cluster.TotalNodes = totalNodes(pools)
	return cluster, warnings
}

// ApplyUpstreamPolicy applies the same node-sizing transform to an upstream
// (Rancher management) cluster.
func ApplyUpstreamPolicy(up types.UpstreamCluster, spec envconfig.SizingTargetSpec) (types.UpstreamCluster, []string) {
	pools, warnings := applyPolicyToPools(up.NodePools, spec, "upstream cluster")
	up.NodePools = pools
	up.TotalNodes = totalNodes(pools)
	return up, warnings
}

// DefaultUpstreamCluster returns the fixed baseline upstream topology — a single
// all-roles node — seeded with the distro/version/cni/provider/env from config.
// The sizing policy then shapes it (e.g. ha -> all-roles x3).
func DefaultUpstreamCluster(cfg envconfig.UpstreamConfig) types.UpstreamCluster {
	pool := types.NodeRequirement{
		Etcd:         true,
		ControlPlane: true,
		Worker:       true,
		Quantity:     1,
		Description:  "upstream all-roles",
	}
	return types.UpstreamCluster{
		NodePools:         []types.NodeRequirement{pool},
		KubernetesDistro:  cfg.KubernetesDistro,
		KubernetesVersion: cfg.KubernetesVersion,
		CNI:               cfg.CNI,
		Provider:          cfg.Provider,
		Env:               cfg.Env,
		TotalNodes:        1,
	}
}

// applyPolicyToPools is the shared node-pool transform used for both upstream
// and downstream clusters. targetLabel is only used in warning messages.
func applyPolicyToPools(pools []types.NodeRequirement, spec envconfig.SizingTargetSpec, targetLabel string) ([]types.NodeRequirement, []string) {
	var warnings []string

	// Track which roles have a dedicated floor still unsatisfied so we can fall
	// back to bumping an all-roles pool when no dedicated pool exists.
	for i := range pools {
		p := &pools[i]
		allRoles := p.Etcd && p.ControlPlane && p.Worker

		// All-roles floor (HA for combined-role clusters).
		if allRoles && spec.MinAllRoles > 0 && p.Quantity < spec.MinAllRoles {
			p.Quantity = spec.MinAllRoles
		}

		// Per-role floors apply to dedicated pools bearing that role. An
		// all-roles pool already covers all roles, so we treat its MinAllRoles
		// (handled above) as the governing floor and additionally honour the
		// max per-role floor here so that, e.g., MinEtcd=3 lifts an all-roles
		// pool to 3 as well.
		floor := 0
		if p.Etcd {
			floor = maxInt(floor, spec.MinEtcd)
		}
		if p.ControlPlane {
			floor = maxInt(floor, spec.MinControlPlane)
		}
		if p.Worker {
			floor = maxInt(floor, spec.MinWorker)
		}
		if floor > 0 && p.Quantity < floor {
			p.Quantity = floor
		}

		// Odd-etcd enforcement for quorum.
		if spec.EnforceOddEtcd && p.Etcd {
			if next := nextOdd(p.Quantity); next != p.Quantity {
				p.Quantity = next
			}
		}

		// Clamp per-node recommended specs to the caps.
		if p.Spec != nil {
			clampSpecToCaps(p.Spec, spec)
		}
	}

	// Total-node cap (HA wins): if the floors push us over the cap, keep the
	// floors and warn. We only attempt to shrink pools that exceed their own
	// role floor.
	if spec.MaxTotalNodes > 0 {
		warnings = append(warnings, enforceTotalCap(pools, spec, targetLabel)...)
	}

	return pools, warnings
}

// enforceTotalCap reduces pool quantities toward their role floors to meet the
// total-node cap. If the floors themselves exceed the cap, it leaves them in
// place and returns a warning (HA wins over cost).
func enforceTotalCap(pools []types.NodeRequirement, spec envconfig.SizingTargetSpec, targetLabel string) []string {
	total := totalNodes(pools)
	if total <= spec.MaxTotalNodes {
		return nil
	}

	// Reduce excess capacity above each pool's floor, largest pools first is
	// unnecessary; a simple pass suffices since we only shrink to floors.
	for i := range pools {
		if total <= spec.MaxTotalNodes {
			break
		}
		p := &pools[i]
		floor := poolFloor(*p, spec)
		if p.Quantity > floor {
			reducible := p.Quantity - floor
			need := total - spec.MaxTotalNodes
			cut := reducible
			if cut > need {
				cut = need
			}
			p.Quantity -= cut
			total -= cut
		}
	}

	if total > spec.MaxTotalNodes {
		return []string{fmt.Sprintf(
			"%s: high-availability floors require %d node(s), exceeding max_total_nodes=%d; applying HA floors (cost cap not enforced)",
			targetLabel, total, spec.MaxTotalNodes)}
	}
	return nil
}

// poolFloor returns the minimum quantity a pool may be reduced to under the
// spec, honouring odd-etcd (an etcd pool can't drop below its odd floor).
func poolFloor(p types.NodeRequirement, spec envconfig.SizingTargetSpec) int {
	floor := 1
	if p.Etcd && p.ControlPlane && p.Worker && spec.MinAllRoles > 0 {
		floor = maxInt(floor, spec.MinAllRoles)
	}
	if p.Etcd {
		floor = maxInt(floor, spec.MinEtcd)
	}
	if p.ControlPlane {
		floor = maxInt(floor, spec.MinControlPlane)
	}
	if p.Worker {
		floor = maxInt(floor, spec.MinWorker)
	}
	if spec.EnforceOddEtcd && p.Etcd {
		floor = nextOdd(floor)
	}
	return floor
}

// clampSpecToCaps clamps a machine spec to the per-node caps in the policy.
// Zero caps mean "no limit".
func clampSpecToCaps(s *types.MachineSpec, spec envconfig.SizingTargetSpec) {
	if spec.MaxNodeVCPUs > 0 && s.VCPUs > spec.MaxNodeVCPUs {
		s.VCPUs = spec.MaxNodeVCPUs
	}
	if spec.MaxNodeMemoryGiB > 0 && s.MemoryGiB > spec.MaxNodeMemoryGiB {
		s.MemoryGiB = spec.MaxNodeMemoryGiB
	}
	if spec.MaxNodeDiskGiB > 0 && s.DiskGiB > spec.MaxNodeDiskGiB {
		s.DiskGiB = spec.MaxNodeDiskGiB
	}
}

// nextOdd returns n if it is already odd (and >= 1), otherwise the next odd
// number above it. nextOdd(0) == 1, nextOdd(2) == 3, nextOdd(4) == 5.
func nextOdd(n int) int {
	if n < 1 {
		return 1
	}
	if n%2 == 1 {
		return n
	}
	return n + 1
}
