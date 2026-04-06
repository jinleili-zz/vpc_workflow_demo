package asynqbroker

import (
	"context"
	"log"

	"github.com/hibiken/asynq"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
)

// ConsumerConfig holds configuration for the asynq consumer.
type ConsumerConfig struct {
	// Concurrency is the number of concurrent worker goroutines.
	Concurrency int
	// Queues maps queue names to their priority weights.
	Queues map[string]int
	// StrictPriority enables strict priority ordering.
	StrictPriority bool
	// Logger is the asynq logger. If nil, asynq's default logger is used.
	Logger asynq.Logger
}

// Consumer implements taskqueue.Consumer using asynq.
type Consumer struct {
	server *asynq.Server
	mux    *asynq.ServeMux
}

// NewConsumer creates an asynq-backed Consumer.
func NewConsumer(opt asynq.RedisConnOpt, cfg ConsumerConfig) *Consumer {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 2
	}

	asynqCfg := asynq.Config{
		Concurrency:    cfg.Concurrency,
		Queues:         cfg.Queues,
		StrictPriority: cfg.StrictPriority,
	}
	if cfg.Logger != nil {
		asynqCfg.Logger = cfg.Logger
	}

	server := asynq.NewServer(opt, asynqCfg)

	return &Consumer{
		server: server,
		mux:    asynq.NewServeMux(),
	}
}

// Handle registers a handler for the given task type.
// The handler receives the broker-level Task reconstructed from the asynq message.
func (c *Consumer) Handle(taskType string, handler taskqueue.HandlerFunc) {
	c.mux.HandleFunc(taskType, func(ctx context.Context, t *asynq.Task) error {
		payload, traceMeta, reply, metadata := unwrapEnvelope(t.Payload())
		ctx = injectTraceFromMetadata(ctx, traceMeta)

		queueName, _ := asynq.GetQueueName(ctx)
		task := &taskqueue.Task{
			Type:     t.Type(),
			Payload:  payload,
			Queue:    queueName,
			Reply:    reply,
			Metadata: metadata,
		}

		if err := handler(ctx, task); err != nil {
			taskID, _ := asynq.GetTaskID(ctx)
			log.Printf("[asynqbroker] handler error: type=%s, task_id=%s, err=%v", taskType, taskID, err)
			return err
		}
		return nil
	})
}

// Start begins consuming messages. This method blocks until Stop is called
// or the context is cancelled.
func (c *Consumer) Start(ctx context.Context) error {
	// Start a goroutine to watch for context cancellation
	go func() {
		<-ctx.Done()
		c.server.Shutdown()
	}()
	return c.server.Run(c.mux)
}

// Stop gracefully shuts down the consumer.
func (c *Consumer) Stop() error {
	c.server.Shutdown()
	return nil
}
