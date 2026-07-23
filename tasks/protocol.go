package tasks

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/jinleili-zz/nsp-platform/taskqueue"

	"workflow_qoder/internal/orchestration"
)

// ValidateTaskProtocol must wrap device handlers so protocol corruption is
// rejected before any device-side read or write occurs.
func ValidateTaskProtocol(next taskqueue.HandlerFunc) taskqueue.HandlerFunc {
	return func(ctx context.Context, task *taskqueue.Task) error {
		if next == nil || task == nil {
			return fmt.Errorf("task protocol validator requires handler and task")
		}
		version := task.Metadata[orchestration.MetadataKeyProtocolVersion]
		if version == "" {
			if legacyTaskProtocolEnabled() {
				return next(ctx, task)
			}
			return fmt.Errorf("legacy task protocol v1 is disabled")
		}
		if version != strconv.Itoa(int(orchestration.TaskProtocolVersion)) {
			return fmt.Errorf("unsupported task protocol version: %s", version)
		}
		if err := validateTaskV2(task); err != nil {
			return err
		}
		return next(ctx, task)
	}
}

func validateTaskV2(task *taskqueue.Task) error {
	metadata := task.Metadata
	required := []string{
		orchestration.MetadataKeyEventID,
		orchestration.MetadataKeyOperationID,
		orchestration.MetadataKeyRootOperationID,
		orchestration.MetadataKeyWorkflowID,
		orchestration.MetadataKeyTaskID,
		orchestration.MetadataKeyGeneration,
		orchestration.MetadataKeyAttempt,
		orchestration.MetadataKeyStepName,
		orchestration.MetadataKeyStepOrdinal,
		orchestration.MetadataKeyOperationKey,
		orchestration.MetadataKeyDesiredHash,
		orchestration.MetadataKeyDeviceType,
		orchestration.MetadataKeyResourceID,
		orchestration.MetadataKeyResourceType,
	}
	for _, key := range required {
		if metadata[key] == "" {
			return fmt.Errorf("task v2 metadata is missing %s", key)
		}
	}
	generation, err := strconv.ParseInt(metadata[orchestration.MetadataKeyGeneration], 10, 64)
	if err != nil || generation <= 0 {
		return fmt.Errorf("task v2 generation is invalid")
	}
	attempt, err := strconv.Atoi(metadata[orchestration.MetadataKeyAttempt])
	if err != nil || attempt < 0 {
		return fmt.Errorf("task v2 attempt is invalid")
	}
	ordinal, err := strconv.Atoi(metadata[orchestration.MetadataKeyStepOrdinal])
	if err != nil || ordinal <= 0 {
		return fmt.Errorf("task v2 step ordinal is invalid")
	}
	stepName := metadata[orchestration.MetadataKeyStepName]
	if stepName != task.Type {
		return fmt.Errorf("task v2 step name does not match task type")
	}
	wantOperationKey := fmt.Sprintf("%s:%s:%s:gen:%d", metadata[orchestration.MetadataKeyDeviceType], stepName, metadata[orchestration.MetadataKeyResourceID], generation)
	if metadata[orchestration.MetadataKeyOperationKey] != wantOperationKey {
		return fmt.Errorf("task v2 operation key does not match identity")
	}
	desiredHash, err := orchestration.DesiredSpecHash(task.Payload)
	if err != nil {
		return err
	}
	if metadata[orchestration.MetadataKeyDesiredHash] != desiredHash {
		return fmt.Errorf("task v2 desired hash does not match payload")
	}
	return nil
}

func legacyTaskProtocolEnabled() bool {
	value := os.Getenv("NSP_WORKFLOW_V1_REPLY_ENABLED")
	if value == "" {
		return false
	}
	enabled, err := strconv.ParseBool(value)
	return err == nil && enabled
}
