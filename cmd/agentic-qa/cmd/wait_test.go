package cmd

import (
	"testing"
	"time"

	"github.com/rancher/tests/internal/agenticqa/types"
)

func TestQueuedJobIsNotTerminal(t *testing.T) {
	if isTerminalStatus(jobStatusQueued) {
		t.Fatal("queued job must remain pending")
	}
}

func TestLocalTestIsTerminal(t *testing.T) {
	if !isTerminalStatus(jobStatusLocalTest) {
		t.Fatal("local-test job must be terminal")
	}
}

func TestPendingPollDurationUsesQueueInterval(t *testing.T) {
	pending := map[int]*types.TriggeredJob{0: {QueueID: intPtr(1)}}
	if got := pendingPollDuration(pending, 2*time.Minute); got != waitQueuePollInterval {
		t.Fatalf("duration = %s", got)
	}
	pending[0].BuildNumber = intPtr(3)
	if got := pendingPollDuration(pending, 2*time.Minute); got != 2*time.Minute {
		t.Fatalf("duration = %s", got)
	}
}

func intPtr(value int) *int { return &value }
