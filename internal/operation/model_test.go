package operation

import "testing"

func TestCanTransitionFollowsCreationAndDeletionStateMachines(t *testing.T) {
	valid := []struct {
		operationType string
		current       Status
		next          Status
	}{
		{"create_vpc", StatusAccepted, StatusDispatching},
		{"create_vpc", StatusAccepted, StatusFailed},
		{"create_vpc", StatusAccepted, StatusCancelled},
		{"create_vpc", StatusDispatching, StatusRunning},
		{"create_vpc", StatusDispatching, StatusFailed},
		{"create_vpc", StatusDispatching, StatusCompensating},
		{"create_vpc", StatusRunning, StatusSucceeded},
		{"create_vpc", StatusRunning, StatusFailed},
		{"create_vpc", StatusRunning, StatusCompensating},
		{"create_vpc", StatusCompensating, StatusCompensated},
		{"create_vpc", StatusCompensating, StatusCompensationFailed},
		{"delete_vpc", StatusAccepted, StatusDispatching},
		{"delete_vpc", StatusAccepted, StatusDeleted},
		{"delete_vpc", StatusAccepted, StatusDeleteFailed},
		{"delete_vpc", StatusDispatching, StatusRunning},
		{"delete_vpc", StatusDispatching, StatusDeleted},
		{"delete_vpc", StatusDispatching, StatusDeleteFailed},
		{"delete_vpc", StatusRunning, StatusDeleted},
		{"delete_vpc", StatusRunning, StatusDeleteFailed},
	}
	for _, transition := range valid {
		if !CanTransition(transition.operationType, transition.current, transition.next) {
			t.Errorf("expected %s %s -> %s to be valid", transition.operationType, transition.current, transition.next)
		}
	}

	invalid := []struct {
		operationType string
		current       Status
		next          Status
	}{
		{"create_vpc", StatusAccepted, StatusRunning},
		{"create_vpc", StatusAccepted, StatusDeleted},
		{"create_vpc", StatusRunning, StatusDeleteFailed},
		{"delete_vpc", StatusAccepted, StatusFailed},
		{"delete_vpc", StatusAccepted, StatusCancelled},
		{"delete_vpc", StatusRunning, StatusSucceeded},
		{"delete_vpc", StatusRunning, StatusCompensating},
		{"create_vpc", StatusDispatching, StatusAccepted},
		{"create_vpc", StatusRunning, StatusDispatching},
		{"create_vpc", StatusCompensating, StatusRunning},
		{"create_vpc", Status("unknown"), StatusRunning},
		{"create_vpc", StatusAccepted, Status("unknown")},
		{"unknown", StatusAccepted, StatusDispatching},
	}
	for _, transition := range invalid {
		if CanTransition(transition.operationType, transition.current, transition.next) {
			t.Errorf("expected %s %s -> %s to be invalid", transition.operationType, transition.current, transition.next)
		}
	}
}

func TestAllTerminalStatusesAreImmutable(t *testing.T) {
	terminals := []Status{
		StatusSucceeded,
		StatusFailed,
		StatusCancelled,
		StatusCompensated,
		StatusCompensationFailed,
		StatusDeleted,
		StatusDeleteFailed,
	}
	for _, status := range terminals {
		if !status.Terminal() {
			t.Errorf("%s should be terminal", status)
		}
		if CanTransition("create_vpc", status, StatusRunning) || CanTransition("delete_vpc", status, StatusRunning) {
			t.Errorf("terminal status %s can transition to running", status)
		}
	}
}
