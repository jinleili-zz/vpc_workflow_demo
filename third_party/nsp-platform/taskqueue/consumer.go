package taskqueue

import "context"

// Consumer abstracts message consumption. Implementations can be backed by
// asynq, RocketMQ, Kafka, or any other message queue.
type Consumer interface {
	// Handle registers a handler function for the given task type.
	Handle(taskType string, handler HandlerFunc)
	// Start begins consuming messages. This is typically blocking.
	Start(ctx context.Context) error
	// Stop gracefully shuts down the consumer.
	Stop() error
}
