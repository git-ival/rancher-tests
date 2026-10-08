package k3k

import (
	"strings"
	"testing"

	management "github.com/rancher/shepherd/clients/rancher/generated/management/v3"
)

// TestTransientConditionsAreNotTerminal is the regression test for a batch that aborted the
// instant it was submitted to Rancher.
//
// Rancher sets Ready=False with "Cluster agent is not connected" from the moment the cluster
// object exists until cattle-cluster-agent dials home. Treating that as fatal made every
// import fail immediately, before the agent had any chance to start.
func TestTransientConditionsAreNotTerminal(t *testing.T) {
	transient := []management.Cluster{
		{Conditions: []management.ClusterCondition{
			{Type: "Ready", Status: "False", Reason: "Disconnected", Message: "Cluster agent is not connected"},
		}},
		{Conditions: []management.ClusterCondition{
			{Type: "Connected", Status: "False", Message: "Waiting for agent to connect"},
		}},
		{Conditions: []management.ClusterCondition{
			{Type: "AgentDeployed", Status: "False", Message: "Waiting for agent deployment"},
		}},
		{State: "pending"},
	}

	for _, cluster := range transient {
		if importedClusterReady(&cluster) {
			t.Fatalf("cluster should not be considered ready: %#v", cluster.Conditions)
		}
		if reason := notReadyReason(&cluster); reason == "" {
			t.Fatalf("a not-ready cluster must report a reason: %#v", cluster.Conditions)
		}
	}
}

// TestNotReadyReasonSurfacesTheAgentMessage keeps the diagnostic that matters in the timeout
// error, so an import that genuinely never connects says so.
func TestNotReadyReasonSurfacesTheAgentMessage(t *testing.T) {
	cluster := &management.Cluster{Conditions: []management.ClusterCondition{
		{Type: "AgentDeployed", Status: "True"},
		{Type: "Ready", Status: "False", Reason: "Disconnected", Message: "Cluster agent is not connected"},
	}}

	reason := notReadyReason(cluster)
	if !strings.Contains(reason, "Cluster agent is not connected") {
		t.Fatalf("reason should carry the Rancher message, got %q", reason)
	}
	if !strings.Contains(reason, "Ready") {
		t.Fatalf("reason should name the condition, got %q", reason)
	}
}

// TestNotReadyReasonReportsMissingConditions covers the case where nothing is explicitly
// false, but the cluster has not reported every required condition yet.
func TestNotReadyReasonReportsMissingConditions(t *testing.T) {
	cluster := &management.Cluster{State: "active", Conditions: []management.ClusterCondition{
		{Type: "Ready", Status: "True"},
		{Type: "AgentDeployed", Status: "True"},
	}}

	reason := notReadyReason(cluster)
	for _, expected := range []string{"SystemAccountCreated", "ServiceAccountSecretsMigrated"} {
		if !strings.Contains(reason, expected) {
			t.Fatalf("reason %q should mention the missing condition %q", reason, expected)
		}
	}
}

// TestNotReadyReasonReportsTerminalError distinguishes a genuinely broken cluster.
func TestNotReadyReasonReportsTerminalError(t *testing.T) {
	cluster := &management.Cluster{
		Transitioning:        "error",
		TransitioningMessage: "failed to reconcile",
	}
	if reason := notReadyReason(cluster); !strings.Contains(reason, "failed to reconcile") {
		t.Fatalf("reason should report the transitioning error, got %q", reason)
	}
}

// TestReadyClusterHasNoReason is the positive case.
func TestReadyClusterHasNoReason(t *testing.T) {
	cluster := &management.Cluster{State: "active", Conditions: []management.ClusterCondition{
		{Type: "AgentDeployed", Status: "True"},
		{Type: "Ready", Status: "True"},
		{Type: "SystemAccountCreated", Status: "True"},
		{Type: "ServiceAccountSecretsMigrated", Status: "True"},
	}}
	if reason := notReadyReason(cluster); reason != "" {
		t.Fatalf("a ready cluster should report no reason, got %q", reason)
	}
	if !importedClusterReady(cluster) {
		t.Fatal("cluster should be ready")
	}
}

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
