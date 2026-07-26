package reconciler

import (
	"testing"

	"workflow_qoder/internal/operation"
)

func TestAggregateStatuses(t *testing.T) {
	tests := []struct {
		name     string
		statuses []operation.Status
		want     operation.Status
	}{
		{name: "all succeeded", statuses: []operation.Status{operation.StatusSucceeded, operation.StatusSucceeded}, want: operation.StatusSucceeded},
		{name: "one running", statuses: []operation.Status{operation.StatusSucceeded, operation.StatusRunning}, want: operation.StatusRunning},
		{name: "one failed", statuses: []operation.Status{operation.StatusSucceeded, operation.StatusFailed}, want: operation.StatusFailed},
		{name: "failed sibling still running", statuses: []operation.Status{operation.StatusFailed, operation.StatusRunning}, want: operation.StatusRunning},
		{name: "compensating", statuses: []operation.Status{operation.StatusCompensated, operation.StatusCompensating}, want: operation.StatusCompensating},
		{name: "all compensated", statuses: []operation.Status{operation.StatusCompensated, operation.StatusCompensated}, want: operation.StatusCompensated},
		{name: "compensation failed", statuses: []operation.Status{operation.StatusCompensated, operation.StatusCompensationFailed}, want: operation.StatusCompensationFailed},
		{name: "inconsistent terminal mix", statuses: []operation.Status{operation.StatusSucceeded, operation.StatusCompensated}, want: operation.StatusCompensationFailed},
		{name: "failed and compensated terminal mix", statuses: []operation.Status{operation.StatusFailed, operation.StatusCompensated}, want: operation.StatusCompensationFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AggregateStatuses(test.statuses); got != test.want {
				t.Fatalf("AggregateStatuses(%v) = %s, want %s", test.statuses, got, test.want)
			}
		})
	}
}

func TestVFWAZRecordStatusCoversCompensationTerminals(t *testing.T) {
	tests := map[operation.Status]string{
		operation.StatusRunning:            "creating",
		operation.StatusSucceeded:          "running",
		operation.StatusFailed:             "failed",
		operation.StatusCompensating:       "compensating",
		operation.StatusCompensated:        "deleted",
		operation.StatusCompensationFailed: "failed",
	}
	for status, want := range tests {
		if got := vfwAZRecordStatus(status); got != want {
			t.Fatalf("vfwAZRecordStatus(%s) = %s, want %s", status, got, want)
		}
	}
}
