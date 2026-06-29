package envplan

import (
	"strings"
	"testing"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/types"
)

func allRolesPool(qty int) types.NodeRequirement {
	return types.NodeRequirement{Etcd: true, ControlPlane: true, Worker: true, Quantity: qty}
}

func TestApplySizingPolicy_MinimalNoOp(t *testing.T) {
	policy := envconfig.GenerateSizingPolicy()
	prof, _, ok := policy.ResolveProfile(types.SizingProfileMinimal)
	if !ok {
		t.Fatal("minimal profile not found")
	}
	in := types.ClusterRequirement{NodePools: []types.NodeRequirement{
		{Etcd: true, Quantity: 1},
		{Worker: true, Quantity: 1},
	}}
	out, warnings := ApplySizingPolicy(in, prof.Downstream)
	if len(warnings) != 0 {
		t.Errorf("minimal should produce no warnings, got %v", warnings)
	}
	if out.NodePools[0].Quantity != 1 || out.NodePools[1].Quantity != 1 {
		t.Errorf("minimal must not change quantities, got %+v", out.NodePools)
	}
	if out.TotalNodes != 2 {
		t.Errorf("expected total 2, got %d", out.TotalNodes)
	}
}

func TestApplySizingPolicy_HAFloorsDedicatedPools(t *testing.T) {
	policy := envconfig.GenerateSizingPolicy()
	prof, _, _ := policy.ResolveProfile(types.SizingProfileHA)
	in := types.ClusterRequirement{NodePools: []types.NodeRequirement{
		{Etcd: true, Quantity: 1},
		{ControlPlane: true, Quantity: 1},
		{Worker: true, Quantity: 1},
	}}
	out, _ := ApplySizingPolicy(in, prof.Downstream)
	if out.NodePools[0].Quantity != 3 {
		t.Errorf("etcd floor: want 3, got %d", out.NodePools[0].Quantity)
	}
	if out.NodePools[1].Quantity != 2 {
		t.Errorf("controlplane floor: want 2, got %d", out.NodePools[1].Quantity)
	}
	if out.NodePools[2].Quantity != 2 {
		t.Errorf("worker floor: want 2, got %d", out.NodePools[2].Quantity)
	}
	if out.TotalNodes != 7 {
		t.Errorf("expected total 7, got %d", out.TotalNodes)
	}
}

func TestApplySizingPolicy_HAAllRolesBumpsToThree(t *testing.T) {
	policy := envconfig.GenerateSizingPolicy()
	prof, _, _ := policy.ResolveProfile(types.SizingProfileHA)
	in := types.ClusterRequirement{NodePools: []types.NodeRequirement{allRolesPool(1)}}
	out, _ := ApplySizingPolicy(in, prof.Downstream)
	if out.NodePools[0].Quantity != 3 {
		t.Errorf("HA all-roles: want 3, got %d", out.NodePools[0].Quantity)
	}
}

func TestApplySizingPolicy_OddEtcd(t *testing.T) {
	spec := envconfig.SizingTargetSpec{EnforceOddEtcd: true}
	cases := map[int]int{1: 1, 2: 3, 3: 3, 4: 5}
	for in, want := range cases {
		cl := types.ClusterRequirement{NodePools: []types.NodeRequirement{{Etcd: true, Quantity: in}}}
		out, _ := ApplySizingPolicy(cl, spec)
		if out.NodePools[0].Quantity != want {
			t.Errorf("odd-etcd(%d): want %d, got %d", in, want, out.NodePools[0].Quantity)
		}
	}
}

func TestApplySizingPolicy_PerNodeCapClampsSpec(t *testing.T) {
	spec := envconfig.SizingTargetSpec{MaxNodeVCPUs: 4, MaxNodeMemoryGiB: 8, MaxNodeDiskGiB: 50}
	cl := types.ClusterRequirement{NodePools: []types.NodeRequirement{
		{Worker: true, Quantity: 1, Spec: &types.MachineSpec{VCPUs: 16, MemoryGiB: 64, DiskGiB: 200}},
	}}
	out, _ := ApplySizingPolicy(cl, spec)
	s := out.NodePools[0].Spec
	if s.VCPUs != 4 || s.MemoryGiB != 8 || s.DiskGiB != 50 {
		t.Errorf("caps not applied: %+v", s)
	}
}

func TestApplySizingPolicy_TotalCapReducible(t *testing.T) {
	// Cap of 3; a worker pool of 5 with no floor can shrink to 1 -> total 3.
	spec := envconfig.SizingTargetSpec{MaxTotalNodes: 3}
	cl := types.ClusterRequirement{NodePools: []types.NodeRequirement{
		allRolesPool(1),
		{Worker: true, Quantity: 5},
	}}
	out, warnings := ApplySizingPolicy(cl, spec)
	if out.TotalNodes > 3 {
		t.Errorf("total cap not met: got %d", out.TotalNodes)
	}
	if len(warnings) != 0 {
		t.Errorf("reducible cap should not warn, got %v", warnings)
	}
}

func TestApplySizingPolicy_TotalCapHAWinsWarns(t *testing.T) {
	// HA floors require 7 nodes but cap is 5: HA wins, warning emitted.
	policy := envconfig.GenerateSizingPolicy()
	prof, _, _ := policy.ResolveProfile(types.SizingProfileHA)
	spec := prof.Downstream
	spec.MaxTotalNodes = 5
	cl := types.ClusterRequirement{NodePools: []types.NodeRequirement{
		{Etcd: true, Quantity: 1},
		{ControlPlane: true, Quantity: 1},
		{Worker: true, Quantity: 1},
	}}
	out, warnings := ApplySizingPolicy(cl, spec)
	if out.TotalNodes != 7 {
		t.Errorf("HA floors must win: want total 7, got %d", out.TotalNodes)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "max_total_nodes") {
		t.Errorf("expected HA-exceeds-cap warning, got %v", warnings)
	}
}

func TestDefaultUpstreamCluster(t *testing.T) {
	cfg := envconfig.GenerateUpstreamConfig()
	up := DefaultUpstreamCluster(cfg)
	if len(up.NodePools) != 1 || up.TotalNodes != 1 {
		t.Fatalf("baseline should be 1 all-roles node, got %+v", up)
	}
	p := up.NodePools[0]
	if !(p.Etcd && p.ControlPlane && p.Worker) {
		t.Errorf("baseline pool should be all-roles, got %+v", p)
	}
	if up.KubernetesDistro != cfg.KubernetesDistro || up.Provider != cfg.Provider {
		t.Errorf("baseline should carry config distro/provider, got %+v", up)
	}
}

func TestApplyUpstreamPolicy_HA(t *testing.T) {
	policy := envconfig.GenerateSizingPolicy()
	prof, _, _ := policy.ResolveProfile(types.SizingProfileHA)
	up := DefaultUpstreamCluster(envconfig.GenerateUpstreamConfig())
	out, _ := ApplyUpstreamPolicy(up, prof.Upstream)
	if out.NodePools[0].Quantity != 3 {
		t.Errorf("upstream HA: want all-roles x3, got %d", out.NodePools[0].Quantity)
	}
	if out.TotalNodes != 3 {
		t.Errorf("expected total 3, got %d", out.TotalNodes)
	}
}
