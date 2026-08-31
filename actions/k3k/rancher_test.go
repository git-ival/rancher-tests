package k3k

import (
	"testing"

	management "github.com/rancher/shepherd/clients/rancher/generated/management/v3"
)

func TestImportedClusterReady(t *testing.T) {
	cluster := &management.Cluster{Conditions: []management.ClusterCondition{
		{Type: "AgentDeployed", Status: "True"},
		{Type: "Ready", Status: "True"},
		{Type: "SystemAccountCreated", Status: "True"},
	}}
	if importedClusterReady(cluster) {
		t.Fatal("cluster should not be ready without ServiceAccountSecretsMigrated")
	}
	cluster.Conditions = append(cluster.Conditions, management.ClusterCondition{Type: "ServiceAccountSecretsMigrated", Status: "True"})
	if !importedClusterReady(cluster) {
		t.Fatal("cluster should be ready")
	}
}
