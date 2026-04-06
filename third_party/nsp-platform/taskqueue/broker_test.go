package taskqueue

import "testing"

func TestTaskFields(t *testing.T) {
	reply := &ReplySpec{Queue: "callback-q"}
	task := &Task{
		Type:     "send_email",
		Payload:  []byte(`{"id":"123"}`),
		Queue:    "high",
		Reply:    reply,
		Priority: PriorityHigh,
		Metadata: map[string]string{
			"tenant": "acme",
		},
	}

	if task.Type != "send_email" {
		t.Fatalf("unexpected task type: %s", task.Type)
	}
	if string(task.Payload) != `{"id":"123"}` {
		t.Fatalf("unexpected payload: %s", string(task.Payload))
	}
	if task.Queue != "high" {
		t.Fatalf("unexpected queue: %s", task.Queue)
	}
	if task.Reply == nil || task.Reply.Queue != "callback-q" {
		t.Fatalf("unexpected reply: %#v", task.Reply)
	}
	if task.Priority != PriorityHigh {
		t.Fatalf("unexpected priority: %d", task.Priority)
	}
	if task.Metadata["tenant"] != "acme" {
		t.Fatalf("unexpected metadata: %#v", task.Metadata)
	}
}

func TestPriorityConstants(t *testing.T) {
	tests := map[string]Priority{
		"low":      PriorityLow,
		"normal":   PriorityNormal,
		"high":     PriorityHigh,
		"critical": PriorityCritical,
	}

	expected := map[string]Priority{
		"low":      1,
		"normal":   3,
		"high":     6,
		"critical": 9,
	}

	for name, got := range tests {
		if got != expected[name] {
			t.Fatalf("%s priority mismatch: got %d want %d", name, got, expected[name])
		}
	}
}
