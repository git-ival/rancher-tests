package envplan

import "github.com/rancher/tests/internal/agenticqa/types"
import "testing"

func node(etcd, cp, worker bool, qty int) types.NodeRequirement {
	return types.NodeRequirement{Etcd: etcd, ControlPlane: cp, Worker: worker, Quantity: qty}
}

func TestMergeClusters_SupersetMaxQuantity(t *testing.T) {
	reqs := []types.ClusterRequirement{
		{NodePools: []types.NodeRequirement{node(true, true, true, 1)}, KubernetesDistro: "rke2", Downstream: true},
		{NodePools: []types.NodeRequirement{node(true, true, true, 3)}, KubernetesDistro: "rke2", Downstream: true},
	}
	merged, _, conflicts := MergeClusters(reqs)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %v", conflicts)
	}
	if len(merged.NodePools) != 1 {
		t.Fatalf("expected 1 merged pool, got %d", len(merged.NodePools))
	}
	if merged.NodePools[0].Quantity != 3 {
		t.Errorf("expected max quantity 3, got %d", merged.NodePools[0].Quantity)
	}
	if merged.TotalNodes != 3 {
		t.Errorf("expected total 3, got %d", merged.TotalNodes)
	}
}

func TestMergeClusters_DistinctRolesAccumulate(t *testing.T) {
	reqs := []types.ClusterRequirement{
		{NodePools: []types.NodeRequirement{node(true, false, false, 3)}, Downstream: true},
		{NodePools: []types.NodeRequirement{node(false, false, true, 2)}, Downstream: true},
	}
	merged, _, conflicts := MergeClusters(reqs)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", conflicts)
	}
	if len(merged.NodePools) != 2 {
		t.Fatalf("expected 2 distinct pools, got %d", len(merged.NodePools))
	}
	if merged.TotalNodes != 5 {
		t.Errorf("expected total 5, got %d", merged.TotalNodes)
	}
}

func TestMergeClusters_ProviderConflict(t *testing.T) {
	reqs := []types.ClusterRequirement{
		{Provider: "aws", Downstream: true},
		{Provider: "azure", Downstream: true},
	}
	_, _, conflicts := MergeClusters(reqs)
	if len(conflicts) == 0 {
		t.Fatal("expected a provider conflict")
	}
}

func TestBuildPlan_SingleStrategy(t *testing.T) {
	reqs := []Requirement{
		{File: "a_test.go", Cluster: types.ClusterRequirement{NodePools: []types.NodeRequirement{node(true, true, true, 1)}, Provider: "aws", Downstream: true}, DerivedBy: "static", Jobs: []string{"job1"}},
		{File: "b_test.go", Cluster: types.ClusterRequirement{NodePools: []types.NodeRequirement{node(true, true, true, 3)}, Provider: "aws", Downstream: true}, DerivedBy: "static", Jobs: []string{"job1"}},
	}
	plan := BuildPlan(reqs, 42)
	if plan.Strategy != types.PlanStrategySingle {
		t.Fatalf("expected single strategy, got %s", plan.Strategy)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(plan.Groups))
	}
	if plan.Groups[0].Name != types.PlanGroupNameAll {
		t.Errorf("expected group name %q, got %q", types.PlanGroupNameAll, plan.Groups[0].Name)
	}
	if plan.Groups[0].Cluster.NodePools[0].Quantity != 3 {
		t.Errorf("expected merged max quantity 3, got %d", plan.Groups[0].Cluster.NodePools[0].Quantity)
	}
}

func TestBuildPlan_PerGroupFallback(t *testing.T) {
	reqs := []Requirement{
		{File: "a_test.go", Cluster: types.ClusterRequirement{Provider: "aws", Downstream: true}, DerivedBy: "static", Jobs: []string{"aws-job"}},
		{File: "b_test.go", Cluster: types.ClusterRequirement{Provider: "azure", Downstream: true}, DerivedBy: "static", Jobs: []string{"azure-job"}},
	}
	plan := BuildPlan(reqs, 7)
	if plan.Strategy != types.PlanStrategyPerGroup {
		t.Fatalf("expected per_group strategy, got %s", plan.Strategy)
	}
	if len(plan.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(plan.Groups))
	}
}
