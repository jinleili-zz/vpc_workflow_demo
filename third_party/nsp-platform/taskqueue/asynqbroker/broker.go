package asynqbroker

import (
	"context"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
)

// Broker implements taskqueue.Broker using asynq.
type Broker struct {
	client *asynq.Client
}

// NewBroker creates an asynq-backed Broker.
func NewBroker(opt asynq.RedisConnOpt) *Broker {
	return &Broker{
		client: asynq.NewClient(opt),
	}
}

// Publish sends a task to the asynq queue.
func (b *Broker) Publish(ctx context.Context, task *taskqueue.Task) (*taskqueue.TaskInfo, error) {
	wrappedPayload := wrapWithTrace(ctx, task.Payload, task.Reply, task.Metadata)
	asynqTask := asynq.NewTask(task.Type, wrappedPayload)

	opts := make([]asynq.Option, 0, 2)
	if task.Queue != "" {
		opts = append(opts, asynq.Queue(task.Queue))
	}

	info, err := b.client.EnqueueContext(ctx, asynqTask, opts...)
	if err != nil {
		return nil, fmt.Errorf("asynq enqueue failed: %w", err)
	}

	return &taskqueue.TaskInfo{
		BrokerTaskID: info.ID,
		Queue:        info.Queue,
	}, nil
}

// Close closes the underlying asynq client.
func (b *Broker) Close() error {
	return b.client.Close()
}
