package taskqueue

// Priority defines task execution priority levels.
type Priority int

const (
	PriorityLow      Priority = 1
	PriorityNormal   Priority = 3
	PriorityHigh     Priority = 6
	PriorityCritical Priority = 9
)

// ReplySpec describes how a worker should route a follow-up reply.
type ReplySpec struct {
	Queue string
}

// Task represents a task to be published to the message queue.
type Task struct {
	Type     string            // Task type identifier, e.g. "create_vrf_on_switch"
	Payload  []byte            // JSON-serialized payload
	Queue    string            // Target queue name
	Reply    *ReplySpec        // Optional reply routing specification
	Priority Priority          // Execution priority
	Metadata map[string]string // Extensible metadata
}

// TaskInfo is returned after a task is successfully published.
type TaskInfo struct {
	BrokerTaskID string // ID assigned by the underlying message queue
	Queue        string // The actual queue the task was placed into
}
