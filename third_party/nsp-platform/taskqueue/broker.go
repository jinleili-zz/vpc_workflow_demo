package taskqueue

import "context"

// Broker abstracts message publishing. Implementations can be backed by
// asynq, RocketMQ, Kafka, or any other message queue.
type Broker interface {
	// Publish sends a task to the message queue.
	Publish(ctx context.Context, task *Task) (*TaskInfo, error)
	// Close releases broker resources.
	Close() error
}
