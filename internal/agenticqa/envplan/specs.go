package envplan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rancher/tests/internal/agenticqa/types"
)

// Heuristic spec baselines. These are deliberately conservative "minimum
// viable" sizes for Rancher downstream node roles, expressed in abstract
// vCPU / memory (GiB) / disk (GiB). Provider mapping happens later.
const (
	// etcd/controlplane nodes are control-plane components: memory-sensitive,
	// modest CPU, but need headroom for the API server and etcd.
	baseControlVCPUs   = 2
	baseControlMemGiB  = 8
	baseControlDiskGiB = 40

	// worker nodes run the actual test workloads.
	baseWorkerVCPUs   = 2
	baseWorkerMemGiB  = 8
	baseWorkerDiskGiB = 40

	// all-roles nodes carry both control-plane and workload duties, so they get
	// the larger of the two baselines plus a little headroom.
	baseAllRolesVCPUs   = 4
	baseAllRolesMemGiB  = 16
	baseAllRolesDiskGiB = 50

	// windows worker nodes need more disk for the larger base images.
	baseWindowsDiskGiB = 80

	// Per-workload increments applied to worker / all-roles pools.
	//
	// Memory is the primary resource consumed by test workloads; each weighted
	// unit of workload pressure adds 1 GiB. This keeps memory growth within
	// catalog tier boundaries instead of jumping an entire tier per workload.
	perWorkloadMemGiB  = 1
	perWorkloadDiskGiB = 5
	// CPU only increments once per cpuPerNWorkloads weighted workload units.
	// A higher threshold prevents a handful of generic workloads from forcing
	// a catalog jump from 2→4 or 4→8 vCPUs. Typical test groups have 2-5
	// workload units; cpu should only rise above baseline for very heavy groups.
	cpuPerNWorkloads = 8

	// Caps so heuristics never recommend an absurdly large node.
	maxHeuristicVCPUs   = 16
	maxHeuristicMemGiB  = 64
	maxHeuristicDiskGiB = 100
)

// workloadWeight lets certain resource-intensive workload kinds count for more
// than one generic workload when sizing. Keys are lowercased Kind values.
var workloadWeight = map[string]int{
	types.WorkloadStatefulSet: 2,
	types.WorkloadHPA:         2,
	types.WorkloadDaemonSet:   2,
	types.WorkloadDeployment:  1,
	types.WorkloadPod:         1,
}

// ComputeSpecs assigns a heuristic MachineSpec to every node pool in the
// cluster, sizing workers/all-roles pools according to the group's workloads.
// It mutates the pools' Spec fields and returns the cluster for chaining.
func ComputeSpecs(cluster types.ClusterRequirement, workloads []types.WorkloadRequirement) types.ClusterRequirement {
	weight := workloadPressure(workloads)

	for i := range cluster.NodePools {
		p := &cluster.NodePools[i]
		spec := baselineFor(*p)

		// Workload pressure only affects pools that schedule workloads
		// (worker or all-roles); dedicated etcd/controlplane pools are not
		// scaled by workload count.
		if p.Worker {
			spec.VCPUs += weight / cpuPerNWorkloads
			spec.MemoryGiB += weight * perWorkloadMemGiB
			spec.DiskGiB += weight * perWorkloadDiskGiB
		}

		clampSpec(&spec)
		spec.Source = types.SpecSourceHeuristic
		spec.Rationale = rationaleFor(*p, weight)
		p.Spec = &spec
	}
	return cluster
}

// baselineFor returns the role-based baseline spec for a single pool.
func baselineFor(p types.NodeRequirement) types.MachineSpec {
	allRoles := p.Etcd && p.ControlPlane && p.Worker
	switch {
	case allRoles:
		s := types.MachineSpec{VCPUs: baseAllRolesVCPUs, MemoryGiB: baseAllRolesMemGiB, DiskGiB: baseAllRolesDiskGiB}
		if p.Windows {
			s.DiskGiB = maxInt(s.DiskGiB, baseWindowsDiskGiB)
		}
		return s
	case p.Worker:
		s := types.MachineSpec{VCPUs: baseWorkerVCPUs, MemoryGiB: baseWorkerMemGiB, DiskGiB: baseWorkerDiskGiB}
		if p.Windows {
			s.DiskGiB = maxInt(s.DiskGiB, baseWindowsDiskGiB)
		}
		return s
	default:
		// etcd and/or controlplane only.
		return types.MachineSpec{VCPUs: baseControlVCPUs, MemoryGiB: baseControlMemGiB, DiskGiB: baseControlDiskGiB}
	}
}

// workloadPressure sums weighted workload counts for a group.
func workloadPressure(workloads []types.WorkloadRequirement) int {
	total := 0
	for _, w := range workloads {
		if wgt, ok := workloadWeight[strings.ToLower(w.Kind)]; ok {
			total += wgt
		} else {
			total += 1
		}
	}
	return total
}

func rationaleFor(p types.NodeRequirement, weight int) string {
	role := roleLabel(p)
	if p.Worker {
		return fmt.Sprintf("baseline for %s role + workload pressure %d", role, weight)
	}
	return fmt.Sprintf("baseline for %s role", role)
}

func roleLabel(p types.NodeRequirement) string {
	var roles []string
	if p.Etcd {
		roles = append(roles, types.RoleEtcd)
	}
	if p.ControlPlane {
		roles = append(roles, types.RoleControlPlane)
	}
	if p.Worker {
		roles = append(roles, types.RoleWorker)
	}
	if p.Windows {
		roles = append(roles, types.RoleWindows)
	}
	if len(roles) == 0 {
		return "unspecified"
	}
	sort.Strings(roles)
	return strings.Join(roles, "+")
}

func clampSpec(s *types.MachineSpec) {
	if s.VCPUs < 1 {
		s.VCPUs = 1
	}
	if s.MemoryGiB < 1 {
		s.MemoryGiB = 1
	}
	if s.DiskGiB < 1 {
		s.DiskGiB = 1
	}
	if s.VCPUs > maxHeuristicVCPUs {
		s.VCPUs = maxHeuristicVCPUs
	}
	if s.MemoryGiB > maxHeuristicMemGiB {
		s.MemoryGiB = maxHeuristicMemGiB
	}
	if s.DiskGiB > maxHeuristicDiskGiB {
		s.DiskGiB = maxHeuristicDiskGiB
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// MaxSpec returns the element-wise maximum of two specs, preserving the source
// of whichever contributed each dimension's larger value where possible. Used
// when merging requirements with the same role signature.
func MaxSpec(a, b *types.MachineSpec) *types.MachineSpec {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := *a
	out.VCPUs = maxInt(a.VCPUs, b.VCPUs)
	out.MemoryGiB = maxInt(a.MemoryGiB, b.MemoryGiB)
	out.DiskGiB = maxInt(a.DiskGiB, b.DiskGiB)
	return &out
}
