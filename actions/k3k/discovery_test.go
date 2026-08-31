package k3k

import (
	"testing"

	"github.com/rancher/norman/types"
	management "github.com/rancher/shepherd/clients/rancher/generated/management/v3"
)

func TestClassifyCluster(t *testing.T) {
	tests := []struct {
		name    string
		cluster *management.Cluster
		want    string
	}{
		{name: "local", cluster: &management.Cluster{Resource: types.Resource{ID: "local"}}, want: "local"},
		{name: "harvester before imported", cluster: &management.Cluster{Labels: map[string]string{"provider.cattle.io": "harvester"}, Driver: "imported"}, want: "harvester"},
		{name: "hosted", cluster: &management.Cluster{Info: &management.ClusterInfo{MachineProvider: "EKS"}}, want: "eks"},
		{name: "hosted operator", cluster: &management.Cluster{Info: &management.ClusterInfo{MachineProvider: "eks-operator"}}, want: "eks"},
		{name: "imported hosted", cluster: &management.Cluster{EKSConfig: &management.EKSClusterConfigSpec{Imported: true}}, want: "imported"},
		{name: "custom", cluster: &management.Cluster{Info: &management.ClusterInfo{MachineProvider: "custom"}}, want: "custom"},
		{name: "node driver", cluster: &management.Cluster{Info: &management.ClusterInfo{MachineProvider: "Amazonec2"}}, want: "nodedriver"},
		{name: "legacy imported", cluster: &management.Cluster{Driver: "imported"}, want: "imported"},
		{name: "unknown", cluster: &management.Cluster{}, want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyCluster(tt.cluster); got != tt.want {
				t.Fatalf("classifyCluster() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTargetMatches(t *testing.T) {
	cluster := &management.Cluster{Name: "host-one", State: "active", Info: &management.ClusterInfo{MachineProvider: "eks"}}
	if !targetMatches(cluster, classifyCluster(cluster), &TargetDiscovery{Prefix: "host-", Types: []string{"hosted"}}) {
		t.Fatal("hosted selector did not match EKS")
	}
	cluster.Labels = map[string]string{"provider.cattle.io": "k3k"}
	if targetMatches(cluster, classifyCluster(cluster), &TargetDiscovery{Prefix: "host-", Types: []string{"hosted"}}) {
		t.Fatal("K3k downstream cluster should not be selected as a host")
	}
	cluster.Labels = nil
	cluster.State = "disconnected"
	if targetMatches(cluster, classifyCluster(cluster), &TargetDiscovery{Prefix: "host-", Types: []string{"hosted"}}) {
		t.Fatal("inactive cluster should not be selected as a host")
	}
}

func TestSelectNodeAddress(t *testing.T) {
	nodes := []management.Node{
		{Name: "b", State: "active", ExternalIPAddress: "192.0.2.2", IPAddress: "10.0.0.2"},
		{Name: "a", State: "active", ExternalIPAddress: "192.0.2.1", IPAddress: "10.0.0.1"},
	}
	if got := selectNodeAddress(nodes, &TargetDiscovery{Address: "external"}); got != "192.0.2.1" {
		t.Fatalf("unexpected external address %q", got)
	}
	if got := selectNodeAddress(nodes, &TargetDiscovery{Address: "internal"}); got != "10.0.0.1" {
		t.Fatalf("unexpected internal address %q", got)
	}
}
