package envplan

import "testing"

// realistic snippet modelled on validation/provisioning/rke2/ace_test.go
const aceSrc = `//go:build validation || ace
package rke2
import (
	"github.com/rancher/tests/actions/clusters"
	"github.com/rancher/tests/actions/config/defaults"
	"github.com/rancher/tests/actions/provisioning"
	"github.com/rancher/tests/actions/provisioninginput"
	"github.com/rancher/tests/actions/workloads/deployment"
	"github.com/rancher/tests/actions/workloads/pods"
)
func TestACE(t *testing.T) {
	r.cattleConfig, err = defaults.SetK8sDefault(client, defaults.RKE2, r.cattleConfig)
	nodeRolesStandard := []provisioninginput.MachinePools{
		provisioninginput.EtcdMachinePool,
		provisioninginput.ControlPlaneMachinePool,
		provisioninginput.WorkerMachinePool,
	}
	nodeRolesStandard[0].MachinePoolConfig.Quantity = 3
	nodeRolesStandard[1].MachinePoolConfig.Quantity = 2
	nodeRolesStandard[2].MachinePoolConfig.Quantity = 3
	clusterConfig.Networking = &provisioninginput.Networking{
		LocalClusterAuthEndpoint: &v1.LocalClusterAuthEndpoint{Enabled: true},
	}
}`

func TestAnalyzeSource_ACE(t *testing.T) {
	a := AnalyzeSource(aceSrc)
	if a.Inconclusive {
		t.Fatal("expected conclusive analysis for ACE test")
	}
	if got := len(a.Cluster.NodePools); got != 3 {
		t.Fatalf("expected 3 node pools, got %d", got)
	}
	// etcd=3, controlplane=2, worker=3
	want := []int{3, 2, 3}
	for i, p := range a.Cluster.NodePools {
		if int(p.Quantity) != want[i] {
			t.Errorf("pool %d: expected quantity %d, got %d", i, want[i], p.Quantity)
		}
	}
	if a.Cluster.TotalNodes != 8 {
		t.Errorf("expected 8 total nodes, got %d", a.Cluster.TotalNodes)
	}
	if a.Cluster.KubernetesDistro != "rke2" {
		t.Errorf("expected rke2 distro, got %q", a.Cluster.KubernetesDistro)
	}
	if a.Cluster.Networking == nil || !a.Cluster.Networking.LocalClusterAuthEndpoint {
		t.Error("expected ACE (LocalClusterAuthEndpoint) networking requirement")
	}
	if !a.Cluster.Downstream {
		t.Error("expected downstream=true")
	}
	// deployment + pod workloads
	kinds := map[string]bool{}
	for _, w := range a.Workloads {
		kinds[w.Kind] = true
	}
	if !kinds["deployment"] || !kinds["pod"] {
		t.Errorf("expected deployment and pod workloads, got %v", kinds)
	}
}

func TestAnalyzeSource_DetectsCharts(t *testing.T) {
	src := `//go:build validation
package charts
import (
	"github.com/rancher/tests/actions/charts"
	"github.com/rancher/tests/actions/provisioninginput"
)
func TestMonitoring(t *testing.T) {
	_ = provisioninginput.AllRolesMachinePool
	chartName := charts.RancherMonitoringName
	_ = charts.LonghornChartName
}`
	a := AnalyzeSource(src)
	if a.Inconclusive {
		t.Fatal("expected conclusive analysis")
	}
	names := map[string]bool{}
	for _, c := range a.Charts {
		names[c.Name] = true
	}
	if !names["rancher-monitoring"] {
		t.Errorf("expected rancher-monitoring chart detected, got %v", a.Charts)
	}
	if !names["longhorn"] {
		t.Errorf("expected longhorn chart detected, got %v", a.Charts)
	}
}

func TestAnalyzeSource_ChartOnlyIsConclusive(t *testing.T) {
	// A file that only references a chart (no node pools/distro) is still
	// conclusive because charts drive sizing.
	src := `package x
	import "github.com/rancher/tests/actions/charts"
	var c = charts.NeuVectorChartName`
	a := AnalyzeSource(src)
	if a.Inconclusive {
		t.Fatal("chart reference should make analysis conclusive")
	}
	if len(a.Charts) != 1 || a.Charts[0].Name != "neuvector" {
		t.Errorf("expected neuvector chart, got %v", a.Charts)
	}
}

func TestAnalyzeSource_AllRolesDefaultQuantity(t *testing.T) {
	src := `package x
	import "github.com/rancher/tests/actions/provisioninginput"
	var p = provisioninginput.AllRolesMachinePool`
	a := AnalyzeSource(src)
	if a.Inconclusive {
		t.Fatal("expected conclusive")
	}
	if len(a.Cluster.NodePools) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(a.Cluster.NodePools))
	}
	p := a.Cluster.NodePools[0]
	if !(p.Etcd && p.ControlPlane && p.Worker) {
		t.Errorf("expected all roles, got %+v", p)
	}
	if p.Quantity != 1 {
		t.Errorf("expected default quantity 1, got %d", p.Quantity)
	}
}

func TestAnalyzeSource_Inconclusive(t *testing.T) {
	src := `package x
	import "testing"
	func TestNothing(t *testing.T) { _ = 1 + 1 }`
	a := AnalyzeSource(src)
	if !a.Inconclusive {
		t.Errorf("expected inconclusive analysis, got %+v", a)
	}
}

func TestAnalyzeSource_InlineNodeLiterals(t *testing.T) {
	// Models validation/provisioning/k3s/custom_test.go's tfpConfig.Nodepool style.
	src := `package x
	import "github.com/rancher/tests/actions/config/defaults"
	func TestK3SCustom(t *testing.T) {
		_, _ = defaults.SetK8sDefault(c, defaults.K3S, cfg)
		nodeRolesAll := []tfpConfig.Nodepool{{Quantity: 1, Etcd: true, Controlplane: true, Worker: true}}
		nodeRolesDedicated := []tfpConfig.Nodepool{{Quantity: 3, Etcd: true}, {Quantity: 2, Controlplane: true}, {Quantity: 3, Worker: true}}
		_ = nodeRolesAll
		_ = nodeRolesDedicated
	}`
	a := AnalyzeSource(src)
	if a.Inconclusive {
		t.Fatal("expected conclusive")
	}
	if a.Cluster.KubernetesDistro != "k3s" {
		t.Errorf("expected k3s, got %q", a.Cluster.KubernetesDistro)
	}
	// Expect 4 distinct role signatures: all-roles(1), etcd(3), controlplane(2), worker(3)
	sig := map[string]int{}
	for _, p := range a.Cluster.NodePools {
		sig[inlineRoleSignature(p)] = p.Quantity
	}
	wantSig := map[string]int{
		"etcd+controlplane+worker": 1,
		"etcd":                     3,
		"controlplane":             2,
		"worker":                   3,
	}
	for k, want := range wantSig {
		if got := sig[k]; got != want {
			t.Errorf("role %q: expected qty %d, got %d (all: %v)", k, want, got, sig)
		}
	}
	if a.Cluster.TotalNodes != 9 {
		t.Errorf("expected total 9 nodes, got %d", a.Cluster.TotalNodes)
	}
}

func TestAnalyzeSource_ProviderAndCNI(t *testing.T) {
	src := `package x
	import "github.com/rancher/tests/actions/provisioninginput"
	var prov = provisioninginput.AWSProviderName
	var p = provisioninginput.WorkerMachinePool
	// uses calico CNI
	`
	a := AnalyzeSource(src)
	if a.Cluster.Provider != "aws" {
		t.Errorf("expected provider aws, got %q", a.Cluster.Provider)
	}
	if a.Cluster.CNI != "calico" {
		t.Errorf("expected cni calico, got %q", a.Cluster.CNI)
	}
}
