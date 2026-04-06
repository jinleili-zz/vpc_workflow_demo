// Package logger provides a unified logging module for NSP platform microservices.
// fields.go defines standard field key constants for consistent logging across services.
package logger

// Standard field key constants for structured logging.
// Using constants ensures consistency and prevents typos across all microservices.
const (
	// FieldService is the key for service name field.
	// Example: "nsp-order", "nsp-user", "nsp-gateway"
	FieldService = "service"

	// FieldTraceID is the key for distributed tracing trace ID.
	// Used to correlate logs across multiple services in a single request chain.
	FieldTraceID = "trace_id"

	// FieldSpanID is the key for distributed tracing span ID.
	// Used to identify a specific operation within a trace.
	FieldSpanID = "span_id"

	// FieldUserID is the key for authenticated user identifier.
	// Used to track which user initiated the operation.
	FieldUserID = "user_id"

	// FieldRequestID is the key for request identifier.
	// Used to identify a specific request within a service.
	FieldRequestID = "request_id"

	// FieldModule is the key for logical module name within a service.
	// Example: "order-service", "payment-handler", "user-repository"
	FieldModule = "module"

	// FieldMethod is the key for the method or function being executed.
	// Example: "CreateOrder", "ProcessPayment", "GetUserByID"
	FieldMethod = "method"

	// FieldPath is the key for HTTP request path or RPC method path.
	// Example: "/api/v1/orders", "/users/{id}"
	FieldPath = "path"

	// FieldCode is the key for response code or error code.
	// Example: 200, 500, "ORDER_NOT_FOUND"
	FieldCode = "code"

	// FieldLatencyMS is the key for operation latency in milliseconds.
	// Used for performance monitoring and SLA tracking.
	FieldLatencyMS = "latency_ms"

	// FieldError is the key for error message or error details.
	// Used to attach error information to log entries.
	FieldError = "error"

	// FieldPeerAddr is the key for remote peer address.
	// Example: client IP address, downstream service address.
	FieldPeerAddr = "peer_addr"
)

// Access log specific field constants.
// These fields are typically used in HTTP access logs.
const (
	// FieldHTTPMethod is the HTTP request method (GET, POST, PUT, DELETE, etc.)
	FieldHTTPMethod = "http_method"

	// FieldHTTPPath is the HTTP request path
	FieldHTTPPath = "http_path"

	// FieldHTTPStatus is the HTTP response status code
	FieldHTTPStatus = "http_status"

	// FieldHTTPLatency is the request processing latency in milliseconds
	FieldHTTPLatency = "http_latency_ms"

	// FieldHTTPQuery is the HTTP request query string
	FieldHTTPQuery = "http_query"

	// FieldClientIP is the client IP address
	FieldClientIP = "client_ip"

	// FieldUserAgent is the client user agent string
	FieldUserAgent = "user_agent"

	// FieldRequestSize is the request body size in bytes
	FieldRequestSize = "request_size"

	// FieldResponseSize is the response body size in bytes
	FieldResponseSize = "response_size"

	// FieldReferer is the HTTP referer header
	FieldReferer = "referer"
)

// Platform log specific field constants.
// These fields are typically used in framework/infrastructure logs.
const (
	// FieldComponent is the platform component name
	// Example: "asynq", "saga", "redis", "postgres"
	FieldComponent = "component"

	// FieldTaskType is the async task type
	FieldTaskType = "task_type"

	// FieldTaskID is the async task ID
	FieldTaskID = "task_id"

	// FieldQueue is the queue name
	FieldQueue = "queue"

	// FieldWorkerID is the worker identifier
	FieldWorkerID = "worker_id"

	// FieldRetryCount is the retry count for failed operations
	FieldRetryCount = "retry_count"

	// FieldWorkflowID is the workflow/saga instance ID
	FieldWorkflowID = "workflow_id"

	// FieldStepName is the workflow step name
	FieldStepName = "step_name"
)

// Log category field constant.
const (
	// FieldCategory is the log category (access, platform, business)
	FieldCategory = "category"
)
