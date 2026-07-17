package envplan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
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
//
// Backward-compatible wrapper: sizes by workloads only (no charts).
func ComputeSpecs(cluster types.ClusterRequirement, workloads []types.WorkloadRequirement) types.ClusterRequirement {
	return ComputeSpecsWithCharts(cluster, workloads, nil)
}

// ComputeSpecsWithCharts is ComputeSpecs plus Helm-chart footprints. Charts are
// the dominant resource driver (test workload helpers rarely set container
// requests), so their resolved request totals are added to the worker /
// all-roles pools on top of the role baseline and the (minor) workload-count
// term. Unresolved charts contribute a conservative default footprint so an
// undetected-footprint chart still bumps sizing.
func ComputeSpecsWithCharts(cluster types.ClusterRequirement, workloads []types.WorkloadRequirement, charts []types.ChartRequirement) types.ClusterRequirement {
	return computeSpecsWithScaleOut(cluster, workloads, charts, envconfig.SizingTargetSpec{})
}

// ComputeSpecsWithChartsForPolicy scales worker count first, then divides
// aggregate pressure across those nodes.
func ComputeSpecsWithChartsForPolicy(cluster types.ClusterRequirement, workloads []types.WorkloadRequirement, charts []types.ChartRequirement, policy envconfig.SizingTargetSpec) types.ClusterRequirement {
	return computeSpecsWithScaleOut(cluster, workloads, charts, policy)
}

func computeSpecsWithScaleOut(cluster types.ClusterRequirement, workloads []types.WorkloadRequirement, charts []types.ChartRequirement, policy envconfig.SizingTargetSpec) types.ClusterRequirement {
	weight := workloadPressure(workloads)
	chartVCPUs, chartMemGiB, chartDiskGiB, chartNames := chartPressure(charts)
	workerIndex, workers := scaleOutWorkers(&cluster, weight, chartVCPUs, chartMemGiB, chartDiskGiB, policy)
	if workers < 1 {
		workers = 1
	}

	for i := range cluster.NodePools {
		p := &cluster.NodePools[i]
		spec := baselineFor(*p)

		// Workload pressure and chart footprints only affect pools that
		// schedule workloads (worker or all-roles); dedicated etcd/controlplane
		// pools are not scaled by them.
		if p.Worker && (workerIndex < 0 || i == workerIndex) {
			poolWorkers := p.Quantity
			if poolWorkers < 1 {
				poolWorkers = workers
			}
			spec.VCPUs += ceilDiv(weight, poolWorkers*cpuPerNWorkloads)
			spec.MemoryGiB += ceilDiv(weight*perWorkloadMemGiB, poolWorkers)
			spec.DiskGiB += ceilDiv(weight*perWorkloadDiskGiB, poolWorkers)

			spec.VCPUs += ceilDiv(chartVCPUs, poolWorkers)
			spec.MemoryGiB += ceilDiv(chartMemGiB, poolWorkers)
			spec.DiskGiB += ceilDiv(chartDiskGiB, poolWorkers)
		}

		clampSpec(&spec)
		spec.Source = types.SpecSourceHeuristic
		spec.Rationale = rationaleForFull(*p, weight, chartNames, chartVCPUs, chartMemGiB)
		p.Spec = &spec
	}
	cluster.TotalNodes = totalNodes(cluster.NodePools)
	return cluster
}

func scaleOutWorkers(cluster *types.ClusterRequirement, weight, chartVCPUs, chartMemGiB, chartDiskGiB int, policy envconfig.SizingTargetSpec) (int, int) {
	required := 1
	apply := func(pressure, target int) {
		if pressure > 0 && target > 0 {
			required = maxInt(required, ceilDiv(pressure, target))
		}
	}
	apply(weight, policy.WorkloadUnitsPerNode)
	apply(chartVCPUs, policy.ChartVCPUsPerNode)
	apply(chartMemGiB, policy.ChartMemoryGiBPerNode)
	apply(chartDiskGiB, policy.ChartDiskGiBPerNode)

	workerIndex := -1
	for i, p := range cluster.NodePools {
		if p.Worker && !(p.Etcd || p.ControlPlane) {
			workerIndex = i
			break
		}
	}
	if workerIndex < 0 {
		for i, p := range cluster.NodePools {
			if p.Worker {
				workerIndex = i
				break
			}
		}
	}
	if workerIndex < 0 {
		return -1, 0
	}
	p := &cluster.NodePools[workerIndex]
	if p.Quantity < required {
		p.Quantity = required
	}
	return workerIndex, p.Quantity
}

// Chart footprint sizing constants.
const (
	// chartHeadroomNumerator/Denominator add scheduling headroom on top of the
	// raw chart request totals (charts also have limits/bursting and the node
	// runs system pods). 3/2 = +50%.
	chartHeadroomNumerator   = 3
	chartHeadroomDenominator = 2
	// defaultUnresolvedChartCPUMillis/MemMiB/DiskGiB are assumed for a detected
	// chart whose footprint could not be resolved from annotations/catalog/LLM.
	// Conservative-but-non-trivial so an unknown chart still raises sizing.
	defaultUnresolvedChartCPUMillis = 1000
	defaultUnresolvedChartMemMiB    = 1024
	defaultUnresolvedChartDiskGiB   = 10
	millisPerVCPU                   = 1000
)

// chartPressure converts the resolved chart footprints into additional whole
// vCPUs / GiB memory / GiB disk to add to worker pools, applying headroom and
// rounding up. Returns the increments and the contributing chart names.
func chartPressure(charts []types.ChartRequirement) (vcpus, memGiB, diskGiB int, names []string) {
	if len(charts) == 0 {
		return 0, 0, 0, nil
	}
	totalCPUMillis, totalMemMiB, totalDiskGiB := 0, 0, 0
	for _, c := range charts {
		fp := c.Footprint
		if fp == nil {
			totalCPUMillis += defaultUnresolvedChartCPUMillis
			totalMemMiB += defaultUnresolvedChartMemMiB
			totalDiskGiB += defaultUnresolvedChartDiskGiB
		} else {
			totalCPUMillis += fp.CPUMillis
			totalMemMiB += fp.MemoryMiB
			totalDiskGiB += fp.DiskGiB
		}
		names = append(names, c.Name)
	}
	// Apply headroom.
	totalCPUMillis = totalCPUMillis * chartHeadroomNumerator / chartHeadroomDenominator
	totalMemMiB = totalMemMiB * chartHeadroomNumerator / chartHeadroomDenominator
	totalDiskGiB = totalDiskGiB * chartHeadroomNumerator / chartHeadroomDenominator

	vcpus = ceilDiv(totalCPUMillis, millisPerVCPU)
	memGiB = ceilDiv(totalMemMiB, giBToMiBSpecs)
	diskGiB = totalDiskGiB
	return vcpus, memGiB, diskGiB, names
}

// giBToMiBSpecs converts GiB↔MiB for chart memory math (kept local to specs).
const giBToMiBSpecs = 1024

// ceilDiv returns ceil(a/b) for non-negative ints (b>0).
func ceilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
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

// rationaleForFull explains a worker pool's spec including chart contributions.
func rationaleForFull(p types.NodeRequirement, weight int, chartNames []string, chartVCPUs, chartMemGiB int) string {
	role := roleLabel(p)
	if !p.Worker {
		return fmt.Sprintf("baseline for %s role", role)
	}
	if len(chartNames) == 0 {
		return fmt.Sprintf("baseline for %s role + workload pressure %d", role, weight)
	}
	return fmt.Sprintf("baseline for %s role + workload pressure %d + charts [%s] (+%d vCPU, +%d GiB)",
		role, weight, strings.Join(chartNames, ", "), chartVCPUs, chartMemGiB)
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
