package envplan

import (
	"testing"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/types"
)

func TestComputeSpecs_RoleBaselines(t *testing.T) {
	cluster := types.ClusterRequirement{
		NodePools: []types.NodeRequirement{
			{Etcd: true, Quantity: 3},                                   // control-plane class
			{Worker: true, Quantity: 3},                                 // worker class
			{Etcd: true, ControlPlane: true, Worker: true, Quantity: 1}, // all-roles
		},
	}
	out := ComputeSpecs(cluster, nil)

	etcd := out.NodePools[0].Spec
	if etcd == nil || etcd.VCPUs != baseControlVCPUs || etcd.MemoryGiB != baseControlMemGiB {
		t.Errorf("etcd pool spec = %+v, want control baseline", etcd)
	}
	worker := out.NodePools[1].Spec
	if worker == nil || worker.VCPUs != baseWorkerVCPUs {
		t.Errorf("worker pool spec = %+v, want worker baseline", worker)
	}
	all := out.NodePools[2].Spec
	if all == nil || all.VCPUs != baseAllRolesVCPUs || all.MemoryGiB != baseAllRolesMemGiB {
		t.Errorf("all-roles pool spec = %+v, want all-roles baseline", all)
	}
	for _, p := range out.NodePools {
		if p.Spec.Source != "heuristic" {
			t.Errorf("expected source heuristic, got %q", p.Spec.Source)
		}
	}
}

func TestComputeSpecs_WorkloadPressureScalesWorkers(t *testing.T) {
	pools := []types.NodeRequirement{{Worker: true, Quantity: 2}}
	light := ComputeSpecs(types.ClusterRequirement{NodePools: clone(pools)}, nil)
	heavy := ComputeSpecs(types.ClusterRequirement{NodePools: clone(pools)}, []types.WorkloadRequirement{
		{Kind: "statefulset"}, {Kind: "statefulset"}, {Kind: "deployment"},
		{Kind: "horizontalpodautoscaler"}, {Kind: "daemonset"},
	})

	lw := light.NodePools[0].Spec
	hw := heavy.NodePools[0].Spec
	if !(hw.MemoryGiB > lw.MemoryGiB && hw.DiskGiB > lw.DiskGiB) {
		t.Errorf("heavy workload pool (%+v) should exceed light (%+v) in mem/disk", hw, lw)
	}
}

func TestComputeSpecsForPolicyScalesOutWorkers(t *testing.T) {
	profile, _, _ := envconfig.GenerateSizingPolicy().ResolveProfile(types.SizingProfileBalanced)
	workloads := []types.WorkloadRequirement{
		{Kind: types.WorkloadStatefulSet},
		{Kind: types.WorkloadStatefulSet},
		{Kind: types.WorkloadDaemonSet},
		{Kind: types.WorkloadDeployment},
	}
	cluster := types.ClusterRequirement{NodePools: []types.NodeRequirement{{Worker: true, Quantity: 1}}}
	out := ComputeSpecsWithChartsForPolicy(cluster, workloads, nil, profile.Downstream)

	if out.NodePools[0].Quantity != 3 {
		t.Fatalf("worker quantity = %d, want 3", out.NodePools[0].Quantity)
	}
	spec := out.NodePools[0].Spec
	if spec.MemoryGiB >= baseWorkerMemGiB+workloadPressure(workloads) {
		t.Fatalf("workload pressure was not spread across nodes: %+v", spec)
	}
}

func TestComputeSpecsForPolicyPrefersDedicatedWorkers(t *testing.T) {
	profile, _, _ := envconfig.GenerateSizingPolicy().ResolveProfile(types.SizingProfileBalanced)
	charts := []types.ChartRequirement{{Name: "monitoring", Footprint: &types.ChartFootprint{CPUMillis: 6000, MemoryMiB: 12288, DiskGiB: 30}}}
	cluster := types.ClusterRequirement{NodePools: []types.NodeRequirement{
		{Etcd: true, ControlPlane: true, Worker: true, Quantity: 1},
		{Worker: true, Quantity: 1},
	}}
	out := ComputeSpecsWithChartsForPolicy(cluster, nil, charts, profile.Downstream)

	if out.NodePools[0].Quantity != 1 {
		t.Fatalf("all-roles quantity = %d, want 1", out.NodePools[0].Quantity)
	}
	if out.NodePools[1].Quantity <= 1 {
		t.Fatalf("dedicated worker pool did not scale out: %+v", out.NodePools[1])
	}
	if out.NodePools[0].Spec.VCPUs != baseAllRolesVCPUs {
		t.Fatalf("chart pressure applied to all-roles pool despite dedicated workers: %+v", out.NodePools[0].Spec)
	}
}

func TestComputeSpecs_ControlPlaneNotScaledByWorkloads(t *testing.T) {
	pools := []types.NodeRequirement{{Etcd: true, ControlPlane: true, Quantity: 1}}
	a := ComputeSpecs(types.ClusterRequirement{NodePools: clone(pools)}, nil)
	b := ComputeSpecs(types.ClusterRequirement{NodePools: clone(pools)}, []types.WorkloadRequirement{
		{Kind: "deployment"}, {Kind: "deployment"}, {Kind: "deployment"},
	})
	if *a.NodePools[0].Spec != *b.NodePools[0].Spec {
		t.Errorf("control-plane pool should not be scaled by workloads: %+v vs %+v",
			a.NodePools[0].Spec, b.NodePools[0].Spec)
	}
}

func TestComputeSpecs_Caps(t *testing.T) {
	pools := []types.NodeRequirement{{Worker: true, Quantity: 1}}
	heavy := make([]types.WorkloadRequirement, 200)
	for i := range heavy {
		heavy[i] = types.WorkloadRequirement{Kind: "statefulset"}
	}
	out := ComputeSpecs(types.ClusterRequirement{NodePools: pools}, heavy)
	s := out.NodePools[0].Spec
	if s.VCPUs > maxHeuristicVCPUs || s.MemoryGiB > maxHeuristicMemGiB || s.DiskGiB > maxHeuristicDiskGiB {
		t.Errorf("spec exceeded caps: %+v", s)
	}
}

func TestSelectInstanceType(t *testing.T) {
	tmpl := envconfig.Generate().CattleConfig
	// Need >=4 vCPU, >=16 GiB: smallest match is t3.xlarge or m5.xlarge (both
	// 4/16); ranked by name, m5.xlarge < t3.xlarge.
	it, ok := tmpl.SelectInstanceType("aws", 4, 16)
	if !ok {
		t.Fatal("expected a match for 4vCPU/16GiB")
	}
	if it.VCPUs < 4 || it.MemoryGiB < 16 {
		t.Errorf("selected type too small: %+v", it)
	}
	// Memory-heavy: 4 vCPU, 32 GiB → r5.xlarge.
	it, ok = tmpl.SelectInstanceType("aws", 4, 32)
	if !ok || it.MemoryGiB < 32 {
		t.Errorf("expected >=32GiB type, got %+v ok=%v", it, ok)
	}
	// Impossible request returns false.
	if _, ok := tmpl.SelectInstanceType("aws", 999, 999); ok {
		t.Error("expected no match for oversized request")
	}
	// Unknown provider returns false.
	if _, ok := tmpl.SelectInstanceType("nope", 1, 1); ok {
		t.Error("expected no match for unknown provider")
	}
}

func TestApplySpecRefinement_FloorEnforced(t *testing.T) {
	g := &types.EnvironmentGroup{
		Cluster: types.ClusterRequirement{
			NodePools: []types.NodeRequirement{
				{Worker: true, Quantity: 2, Spec: &types.MachineSpec{VCPUs: 4, MemoryGiB: 16, DiskGiB: 50, Source: "heuristic"}},
			},
		},
	}
	// LLM tries to lower memory (8 < 16) but raise disk (80 > 50).
	res := SpecRefinementResult{Pools: []struct {
		Roles     string `json:"roles"`
		VCPUs     int    `json:"vcpus"`
		MemoryGiB int    `json:"memory_gib"`
		DiskGiB   int    `json:"disk_gib"`
		Rationale string `json:"rationale"`
	}{
		{Roles: "worker", VCPUs: 4, MemoryGiB: 8, DiskGiB: 80, Rationale: "x"},
	}}
	n := ApplySpecRefinement(g, res)
	if n != 1 {
		t.Fatalf("expected 1 pool updated, got %d", n)
	}
	s := g.Cluster.NodePools[0].Spec
	if s.MemoryGiB != 16 {
		t.Errorf("memory floor not enforced: got %d want 16", s.MemoryGiB)
	}
	if s.DiskGiB != 80 {
		t.Errorf("disk should be raised to 80, got %d", s.DiskGiB)
	}
	if s.Source != "llm" {
		t.Errorf("expected source llm, got %q", s.Source)
	}
}

func TestApplySpecRefinement_RoleNormalization(t *testing.T) {
	g := &types.EnvironmentGroup{
		Cluster: types.ClusterRequirement{
			NodePools: []types.NodeRequirement{
				{Etcd: true, ControlPlane: true, Worker: true, Quantity: 1, Spec: &types.MachineSpec{VCPUs: 4, MemoryGiB: 16, DiskGiB: 50}},
			},
		},
	}
	// LLM returns roles in a different order/format.
	res := SpecRefinementResult{Pools: []struct {
		Roles     string `json:"roles"`
		VCPUs     int    `json:"vcpus"`
		MemoryGiB int    `json:"memory_gib"`
		DiskGiB   int    `json:"disk_gib"`
		Rationale string `json:"rationale"`
	}{
		{Roles: "worker, etcd, controlplane", VCPUs: 8, MemoryGiB: 32, DiskGiB: 60},
	}}
	if n := ApplySpecRefinement(g, res); n != 1 {
		t.Fatalf("expected role-normalized match, updated %d", n)
	}
	if g.Cluster.NodePools[0].Spec.VCPUs != 8 {
		t.Errorf("expected refined vCPU 8, got %d", g.Cluster.NodePools[0].Spec.VCPUs)
	}
}

func clone(in []types.NodeRequirement) []types.NodeRequirement {
	out := make([]types.NodeRequirement, len(in))
	copy(out, in)
	return out
}
