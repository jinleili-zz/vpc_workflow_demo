package taskqueue

import "context"

// HandlerFunc is the function signature for task handlers.
// It receives the full broker-level task and returns an error if handling fails.
type HandlerFunc func(ctx context.Context, task *Task) error
