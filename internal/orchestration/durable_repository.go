package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"workflow_qoder/internal/models"
)

const TaskProtocolVersion int16 = 2

type WorkflowRepository struct {
	db           *sql.DB
	ownerService string
}

func NewWorkflowRepository(db *sql.DB, ownerService string) *WorkflowRepository {
	return &WorkflowRepository{db: db, ownerService: ownerService}
}

type TaskDispatchPayload struct {
	ProtocolVersion int16             `json:"schema_version"`
	EventID         string            `json:"event_id"`
	TaskID          string            `json:"task_id"`
	TaskType        string            `json:"task_type"`
	StepName        string            `json:"step_name"`
	StepOrdinal     int               `json:"step_ordinal"`
	OperationKey    string            `json:"operation_key"`
	DesiredSpec     json.RawMessage   `json:"desired_spec"`
	DesiredHash     string            `json:"desired_hash"`
	Payload         json.RawMessage   `json:"payload"`
	Queue           string            `json:"queue"`
	ReplyQueue      string            `json:"reply_queue"`
	Priority        int               `json:"priority"`
	MaxRetries      int               `json:"max_retries"`
	Metadata        map[string]string `json:"metadata"`
}

type WorkflowPreparation func(ctx context.Context, tx *sql.Tx) (WorkflowDef, error)

func (r *WorkflowRepository) SubmitWorkflowTx(ctx context.Context, def WorkflowDef, queueResolver QueueResolver, replyQueue string) (string, error) {
	workflowID, _, err := r.SubmitPreparedWorkflowTx(ctx, func(context.Context, *sql.Tx) (WorkflowDef, error) {
		return def, nil
	}, queueResolver, replyQueue)
	return workflowID, err
}

func (r *WorkflowRepository) SubmitPreparedWorkflowTx(ctx context.Context, prepare WorkflowPreparation, queueResolver QueueResolver, replyQueue string) (string, string, error) {
	if r == nil || r.db == nil {
		return "", "", fmt.Errorf("workflow database is required")
	}
	if r.ownerService == "" {
		return "", "", fmt.Errorf("workflow owner service is required")
	}
	if prepare == nil || queueResolver == nil || replyQueue == "" {
		return "", "", fmt.Errorf("workflow preparation, queue, and reply queue are required")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("begin workflow transaction: %w", err)
	}
	defer tx.Rollback()

	def, err := prepare(ctx, tx)
	if err != nil {
		return "", "", fmt.Errorf("prepare workflow resource: %w", err)
	}
	if _, ok := resourceTable(def.ResourceType); !ok {
		return "", "", fmt.Errorf("unsupported resource type: %s", def.ResourceType)
	}
	workflowID := def.WorkflowID
	if workflowID == "" {
		workflowID = def.ResourceID
	}
	operationID := def.OperationID
	if operationID == "" {
		operationID = def.ResourceID
	}
	rootOperationID := def.RootOperationID
	if rootOperationID == "" {
		rootOperationID = operationID
	}
	generation := def.Generation
	if generation == 0 {
		generation = 1
	}
	if workflowID == "" || operationID == "" || def.ResourceID == "" || def.AZ == "" {
		return "", "", fmt.Errorf("workflow identity and resource context are required")
	}
	if def.ReplayExisting {
		if err := validateExistingWorkflow(ctx, tx, def, operationID, rootOperationID, workflowID, generation); err != nil {
			return "", "", err
		}
		if err := tx.Commit(); err != nil {
			return "", "", fmt.Errorf("commit workflow replay: %w", err)
		}
		return workflowID, def.ResourceID, nil
	}
	if len(def.Steps) == 0 {
		return "", "", fmt.Errorf("workflow steps不能为空")
	}

	inserted := 0
	var firstTask *models.Task
	for index, step := range def.Steps {
		task := durableTask(def, operationID, rootOperationID, workflowID, generation, index, step)
		task.Destination = queueResolver(task.DeviceType, task.Priority)
		task.ReplyQueue = replyQueue
		if task.Destination == "" {
			return "", "", fmt.Errorf("queue resolver returned empty destination for step %s", step.TaskType)
		}
		created, err := insertWorkflowTask(ctx, tx, task)
		if err != nil {
			return "", "", fmt.Errorf("persist workflow step %s: %w", step.TaskType, err)
		}
		if !created {
			matches, err := workflowTaskMatches(ctx, tx, task)
			if err != nil {
				return "", "", fmt.Errorf("verify workflow step %s: %w", step.TaskType, err)
			}
			if !matches {
				return "", "", fmt.Errorf("workflow step conflict: %s", step.TaskType)
			}
		} else {
			inserted++
		}
		if index == 0 {
			firstTask = task
		}
	}

	if inserted > 0 {
		if err := updateWorkflowResource(ctx, tx, def.ResourceType, def.ResourceID, len(def.Steps), operationID, generation); err != nil {
			return "", "", err
		}
		if err := r.insertTaskOutbox(ctx, tx, firstTask, len(def.Steps), queueResolver, replyQueue); err != nil {
			return "", "", err
		}
	} else if err := r.verifyTaskOutbox(ctx, tx, firstTask, len(def.Steps), queueResolver); err != nil {
		return "", "", err
	}
	operationResult, err := tx.ExecContext(ctx, `
		UPDATE orchestration_operations
		SET status = 'dispatching', version = version + 1, updated_at = NOW()
		WHERE operation_id = $1 AND resource_id = $2 AND generation = $3
		  AND status = 'accepted' AND operation_type NOT LIKE 'delete_%'
	`, operationID, def.ResourceID, generation)
	if err != nil {
		return "", "", fmt.Errorf("advance operation to dispatching: %w", err)
	}
	if def.OperationRequired {
		rows, err := operationResult.RowsAffected()
		if err != nil {
			return "", "", err
		}
		if rows != 1 {
			var status string
			var resourceID string
			var currentGeneration int64
			if err := tx.QueryRowContext(ctx, `SELECT status, resource_id, generation FROM orchestration_operations WHERE operation_id = $1 FOR UPDATE`, operationID).Scan(&status, &resourceID, &currentGeneration); err != nil {
				return "", "", fmt.Errorf("required workflow operation is missing: %s: %w", operationID, err)
			}
			if resourceID != def.ResourceID || currentGeneration != generation || (status != "dispatching" && status != "running" && status != "succeeded" && status != "failed") {
				return "", "", fmt.Errorf("required workflow operation has incompatible state: %s/%s/generation:%d", operationID, status, currentGeneration)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("commit workflow transaction: %w", err)
	}
	return workflowID, def.ResourceID, nil
}

func validateExistingWorkflow(ctx context.Context, tx *sql.Tx, def WorkflowDef, operationID, rootOperationID, workflowID string, generation int64) error {
	var total, matching int
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (
			WHERE operation_id = $1 AND root_operation_id = $2
			  AND resource_type::text = $3 AND resource_id = $4
		)
		FROM tasks
		WHERE workflow_id = $5 AND generation = $6
	`, operationID, rootOperationID, string(def.ResourceType), def.ResourceID, workflowID, generation).Scan(&total, &matching)
	if err != nil {
		return err
	}
	if total == 0 || matching != total {
		return fmt.Errorf("replayed workflow identity is missing or inconsistent: %s generation=%d", workflowID, generation)
	}
	if def.OperationRequired {
		var resourceID string
		var operationGeneration int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(resource_id, ''), generation FROM orchestration_operations WHERE operation_id = $1 FOR UPDATE`, operationID).Scan(&resourceID, &operationGeneration); err != nil {
			return fmt.Errorf("replayed operation is missing: %s: %w", operationID, err)
		}
		if resourceID != def.ResourceID || operationGeneration != generation {
			return fmt.Errorf("replayed operation identity conflict: %s", operationID)
		}
	}
	return nil
}

func durableTask(def WorkflowDef, operationID, rootOperationID, workflowID string, generation int64, index int, step WorkflowStep) *models.Task {
	maxRetries := step.MaxRetries
	if maxRetries == 0 {
		maxRetries = 3
	}
	stepName := step.TaskType
	taskKey := fmt.Sprintf("%s:%d:%s:%d", workflowID, generation, stepName, index+1)
	return &models.Task{
		ID:                uuid.NewSHA1(uuid.NameSpaceOID, []byte(taskKey)).String(),
		OperationID:       operationID,
		RootOperationID:   rootOperationID,
		WorkflowID:        workflowID,
		Generation:        generation,
		OperationRequired: def.OperationRequired,
		StepName:          stepName,
		ProtocolVersion:   TaskProtocolVersion,
		ResourceType:      def.ResourceType,
		ResourceID:        def.ResourceID,
		TaskType:          step.TaskType,
		TaskName:          step.TaskName,
		TaskOrder:         index + 1,
		TaskParams:        string(step.Payload),
		Status:            models.TaskStatusPending,
		Priority:          step.Priority,
		DeviceType:        step.DeviceType,
		MaxRetries:        maxRetries,
		AZ:                def.AZ,
	}
}

func insertWorkflowTask(ctx context.Context, tx *sql.Tx, task *models.Task) (bool, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO tasks (
			id, operation_id, root_operation_id, workflow_id, generation,
			step_name, attempt, version, protocol_version, operation_required, destination, reply_queue,
			resource_type, resource_id, task_type, task_name, task_order,
			task_params, status, priority, device_type, retry_count, max_retries, az
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17,
			$18::jsonb, $19, $20, $21, $22, $23, $24
		)
		ON CONFLICT (workflow_id, generation, step_name, task_order)
		WHERE protocol_version >= 2
		DO NOTHING
	`, task.ID, task.OperationID, task.RootOperationID, task.WorkflowID, task.Generation,
		task.StepName, task.Attempt, task.Version, task.ProtocolVersion, task.OperationRequired, task.Destination, task.ReplyQueue,
		task.ResourceType, task.ResourceID, task.TaskType, task.TaskName, task.TaskOrder,
		task.TaskParams, task.Status, task.Priority, task.DeviceType, task.RetryCount, task.MaxRetries, task.AZ,
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func workflowTaskMatches(ctx context.Context, tx *sql.Tx, expected *models.Task) (bool, error) {
	var taskID, operationID, rootOperationID, resourceType, resourceID, taskType, taskName, taskParams, deviceType, az, destination, replyQueue string
	var priority, maxRetries int
	err := tx.QueryRowContext(ctx, `
		SELECT id, operation_id, root_operation_id, resource_type::text, resource_id,
		       task_type, task_name, task_params::text, priority, device_type, max_retries, az,
		       destination, reply_queue
		FROM tasks
		WHERE workflow_id = $1 AND generation = $2 AND step_name = $3 AND task_order = $4
	`, expected.WorkflowID, expected.Generation, expected.StepName, expected.TaskOrder).Scan(
		&taskID, &operationID, &rootOperationID, &resourceType, &resourceID,
		&taskType, &taskName, &taskParams, &priority, &deviceType, &maxRetries, &az, &destination, &replyQueue,
	)
	if err != nil {
		return false, err
	}
	var gotPayload, wantPayload any
	if err := json.Unmarshal([]byte(taskParams), &gotPayload); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(expected.TaskParams), &wantPayload); err != nil {
		return false, err
	}
	gotJSON, _ := json.Marshal(gotPayload)
	wantJSON, _ := json.Marshal(wantPayload)
	return taskID == expected.ID && operationID == expected.OperationID && rootOperationID == expected.RootOperationID &&
		resourceType == string(expected.ResourceType) && resourceID == expected.ResourceID &&
		taskType == expected.TaskType && taskName == expected.TaskName && string(gotJSON) == string(wantJSON) &&
		priority == expected.Priority && deviceType == expected.DeviceType && maxRetries == expected.MaxRetries && az == expected.AZ &&
		destination == expected.Destination && replyQueue == expected.ReplyQueue, nil
}

func updateWorkflowResource(ctx context.Context, tx *sql.Tx, resourceType models.ResourceType, resourceID string, totalTasks int, operationID string, generation int64) error {
	table, ok := resourceTable(resourceType)
	if !ok {
		return fmt.Errorf("unsupported resource type: %s", resourceType)
	}
	query := fmt.Sprintf(`UPDATE %s SET status = 'creating', total_tasks = $1, completed_tasks = 0, failed_tasks = 0, error_message = NULL, current_operation_id = $2, generation = $3, version = version + 1, updated_at = NOW() WHERE id = $4`, table)
	result, err := tx.ExecContext(ctx, query, totalTasks, operationID, generation, resourceID)
	if err != nil {
		return fmt.Errorf("update workflow resource: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("workflow resource not found: %s", resourceID)
	}
	return nil
}

func resourceTable(resourceType models.ResourceType) (string, bool) {
	switch resourceType {
	case models.ResourceTypeVPC:
		return "vpc_resources", true
	case models.ResourceTypeSubnet:
		return "subnet_resources", true
	case models.ResourceTypePCCN:
		return "pccn_resources", true
	case models.ResourceTypeFirewallPolicy:
		return "firewall_policies", true
	default:
		return "", false
	}
}

func (r *WorkflowRepository) insertTaskOutbox(ctx context.Context, tx *sql.Tx, task *models.Task, totalSteps int, queueResolver QueueResolver, replyQueue string) error {
	prepared, err := r.prepareTaskOutbox(task, totalSteps, queueResolver)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO outbox_events (
			event_id, event_key, owner_service, aggregate_type, aggregate_id,
			event_type, destination, payload, status
		) VALUES ($1, $2, $3, 'task', $4, 'task.dispatch.v2', $5, $6::jsonb, 'pending')
		ON CONFLICT (event_key) DO NOTHING
	`, prepared.eventID, prepared.eventKey, r.ownerService, task.ID, prepared.destination, prepared.payload)
	if err != nil {
		return fmt.Errorf("insert task outbox: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return r.verifyPreparedTaskOutbox(ctx, tx, task, prepared)
	}
	return nil
}

type preparedTaskOutbox struct {
	eventID     string
	eventKey    string
	destination string
	payload     []byte
}

func (r *WorkflowRepository) prepareTaskOutbox(task *models.Task, totalSteps int, queueResolver QueueResolver) (preparedTaskOutbox, error) {
	destination := task.Destination
	if destination == "" {
		destination = queueResolver(task.DeviceType, task.Priority)
	}
	if destination == "" {
		return preparedTaskOutbox{}, fmt.Errorf("queue resolver returned empty destination")
	}
	eventKey := fmt.Sprintf("task:%s:generation:%d:dispatch", task.ID, task.Generation)
	eventID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(eventKey)).String()
	operationKey := fmt.Sprintf("%s:%s:%s:gen:%d", task.DeviceType, task.StepName, task.ResourceID, task.Generation)
	desiredHash, err := DesiredSpecHash([]byte(task.TaskParams))
	if err != nil {
		return preparedTaskOutbox{}, err
	}
	metadata := map[string]string{
		MetadataKeyProtocolVersion: strconv.Itoa(int(TaskProtocolVersion)),
		MetadataKeyEventID:         eventID,
		MetadataKeyOperationID:     task.OperationID,
		MetadataKeyRootOperationID: task.RootOperationID,
		MetadataKeyWorkflowID:      task.WorkflowID,
		MetadataKeyTaskID:          task.ID,
		MetadataKeyGeneration:      strconv.FormatInt(task.Generation, 10),
		MetadataKeyAttempt:         strconv.Itoa(task.Attempt),
		MetadataKeyStepName:        task.StepName,
		MetadataKeyStepOrdinal:     strconv.Itoa(task.TaskOrder),
		MetadataKeyOperationKey:    operationKey,
		MetadataKeyDesiredHash:     desiredHash,
		MetadataKeyDeviceType:      task.DeviceType,
		MetadataKeyResourceID:      task.ResourceID,
		MetadataKeyResourceType:    string(task.ResourceType),
		MetadataKeyStepIndex:       strconv.Itoa(task.TaskOrder - 1),
		MetadataKeyTotalSteps:      strconv.Itoa(totalSteps),
	}
	payload, err := json.Marshal(TaskDispatchPayload{
		ProtocolVersion: TaskProtocolVersion,
		EventID:         eventID,
		TaskID:          task.ID,
		TaskType:        task.TaskType,
		StepName:        task.StepName,
		StepOrdinal:     task.TaskOrder,
		OperationKey:    operationKey,
		DesiredSpec:     json.RawMessage(task.TaskParams),
		DesiredHash:     desiredHash,
		Payload:         json.RawMessage(task.TaskParams),
		Queue:           destination,
		ReplyQueue:      task.ReplyQueue,
		Priority:        task.Priority,
		MaxRetries:      task.MaxRetries,
		Metadata:        metadata,
	})
	if err != nil {
		return preparedTaskOutbox{}, fmt.Errorf("marshal task outbox payload: %w", err)
	}
	return preparedTaskOutbox{eventID: eventID, eventKey: eventKey, destination: destination, payload: payload}, nil
}

func (r *WorkflowRepository) verifyTaskOutbox(ctx context.Context, tx *sql.Tx, task *models.Task, totalSteps int, queueResolver QueueResolver) error {
	prepared, err := r.prepareTaskOutbox(task, totalSteps, queueResolver)
	if err != nil {
		return err
	}
	return r.verifyPreparedTaskOutbox(ctx, tx, task, prepared)
}

func (r *WorkflowRepository) verifyPreparedTaskOutbox(ctx context.Context, tx *sql.Tx, task *models.Task, expected preparedTaskOutbox) error {
	var eventID, ownerService, aggregateType, aggregateID, eventType, destination, payload string
	err := tx.QueryRowContext(ctx, `
		SELECT event_id, owner_service, aggregate_type, aggregate_id, event_type, destination, payload::text
		FROM outbox_events WHERE event_key = $1 FOR UPDATE
	`, expected.eventKey).Scan(&eventID, &ownerService, &aggregateType, &aggregateID, &eventType, &destination, &payload)
	if err != nil {
		return fmt.Errorf("required task outbox is missing: %s: %w", expected.eventKey, err)
	}
	var gotPayload, wantPayload any
	if err := json.Unmarshal([]byte(payload), &gotPayload); err != nil {
		return err
	}
	if err := json.Unmarshal(expected.payload, &wantPayload); err != nil {
		return err
	}
	gotJSON, _ := json.Marshal(gotPayload)
	wantJSON, _ := json.Marshal(wantPayload)
	if eventID != expected.eventID || ownerService != r.ownerService || aggregateType != "task" || aggregateID != task.ID || eventType != "task.dispatch.v2" || destination != expected.destination || string(gotJSON) != string(wantJSON) {
		return fmt.Errorf("task outbox conflict: %s", expected.eventKey)
	}
	return nil
}

type OutboxEvent struct {
	EventID         string
	EventKey        string
	OwnerService    string
	AggregateType   string
	AggregateID     string
	EventType       string
	Destination     string
	Payload         json.RawMessage
	Status          string
	PublishAttempts int
	AvailableAt     time.Time
}

func (r *WorkflowRepository) RequeueTaskTx(ctx context.Context, taskID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status models.TaskStatus
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT status, generation FROM tasks WHERE id = $1 FOR UPDATE`, taskID).Scan(&status, &generation); err != nil {
		return err
	}
	if status == models.TaskStatusCompleted || status == models.TaskStatusFailed || status == models.TaskStatusCancelled {
		return fmt.Errorf("terminal task requires a new audited operation, not message requeue: %s", taskID)
	}
	eventKey := fmt.Sprintf("task:%s:generation:%d:dispatch", taskID, generation)
	result, err := tx.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = 'retry', available_at = NOW(), locked_at = NULL, locked_by = NULL,
		    published_at = NULL, last_error = 'manual message redelivery', updated_at = NOW()
		WHERE event_key = $1 AND owner_service = $2 AND status != 'publishing'
	`, eventKey, r.ownerService)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("task outbox is missing or currently publishing: %s", taskID)
	}
	return tx.Commit()
}

func (r *WorkflowRepository) ResourceHasActiveOutbox(ctx context.Context, resourceID string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM outbox_events AS event
			JOIN tasks AS task ON task.id = event.aggregate_id
			WHERE event.owner_service = $1 AND task.resource_id = $2
			  AND event.status IN ('pending', 'retry', 'publishing')
		)
	`, r.ownerService, resourceID).Scan(&exists)
	return exists, err
}

func (r *WorkflowRepository) ResourceOperationHasActiveOutbox(ctx context.Context, resourceID, operationID string, generation int64) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM outbox_events AS event
			JOIN tasks AS task ON task.id = event.aggregate_id
			WHERE event.owner_service = $1 AND task.resource_id = $2
			  AND task.operation_id = $3 AND task.generation = $4
			  AND event.status IN ('pending', 'retry', 'publishing')
		)
	`, r.ownerService, resourceID, operationID, generation).Scan(&exists)
	return exists, err
}
